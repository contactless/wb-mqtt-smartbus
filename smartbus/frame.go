// TBD: should rename this module

package smartbus

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"github.com/contactless/wbgo"
	"io"
	"sync"
)

const (
	MIN_FRAME_SIZE = 11 // excluding sync
)

const (
	BROADCAST_SUBNET = 0xff
	BROADCAST_DEVICE = 0xff
)

type MessageHeader struct {
	OrigSubnetID   uint8
	OrigDeviceID   uint8
	OrigDeviceType uint16
	Opcode         uint16
	TargetSubnetID uint8
	TargetDeviceID uint8
}

type MutexLike interface {
	Lock()
	Unlock()
}

type SmartbusMessage struct {
	Header  MessageHeader
	Message interface{}
}

type TimeoutChecker interface {
	IsTimeout(error) bool
}

func isTimeout(stream interface{}, err error) bool {
	checker, ok := stream.(TimeoutChecker)
	return ok && checker.IsTimeout(err)
}

func WritePreBuiltFrame(writer io.Writer, frame []byte) {
	// writing the buffer in parts may cause missed frames
	bs := make([]byte, len(frame)+2)
	bs[0] = 0xaa
	bs[1] = 0xaa
	copy(bs[2:], frame)
	writer.Write(bs)
}

func WriteFrameRaw(writer io.Writer, header MessageHeader, writeMsg func(writer io.Writer)) {
	buf := bytes.NewBuffer(make([]byte, 0, 128))
	binary.Write(buf, binary.BigEndian, uint16(0xaaaa)) // signature
	binary.Write(buf, binary.BigEndian, uint8(0))       // len placeholder
	binary.Write(buf, binary.BigEndian, header)
	writeMsg(buf)
	bs := buf.Bytes()
	bs[2] = uint8(len(bs)) // minus 2 bytes of signature, but plus 2 bytes of crc
	binary.Write(buf, binary.BigEndian, crc16(bs[2:]))
	wbgo.Debug.Printf("sending frame:\n%s", hex.Dump(buf.Bytes()))
	// writing the buffer in parts may cause missed frames
	writer.Write(buf.Bytes())
}

func WriteFrame(writer io.Writer, fullMsg SmartbusMessage) {
	header := fullMsg.Header // make a copy because Opcode field is modified
	msg := fullMsg.Message.(Message)
	header.Opcode = msg.Opcode()

	WriteFrameRaw(writer, header, func(writer io.Writer) {
		var err error
		var preprocessed interface{}
		preprocess, hasPreprocess := msg.(PreprocessedMessage)
		if hasPreprocess {
			preprocessed, err = preprocess.ToRaw()
			if err == nil {
				err = WriteMessage(writer, preprocessed)
			}
		} else {
			err = WriteMessage(writer, msg)
		}
		if err != nil {
			panic(fmt.Sprintf("WriteMessage() failed: %v", err))
		}
	})
}

func ReadSync(reader io.Reader, mutex MutexLike) error {
	var b byte
	for {
		if err := binary.Read(reader, binary.BigEndian, &b); err != nil {
			if isTimeout(reader, err) {
				continue
			}
			return err
		}
		if b != 0xaa {
			wbgo.Debug.Printf("unsync byte 0: %02x", b)
			continue
		}

		mutex.Lock()
		if err := binary.Read(reader, binary.BigEndian, &b); err != nil {
			if isTimeout(reader, err) {
				wbgo.Debug.Printf("sync byte 1 timeout")
				continue
			}
			mutex.Unlock()
			return err
		}
		if b == 0xaa {
			break
		}

		wbgo.Debug.Printf("unsync byte 1: %02x", b)
		mutex.Unlock()
	}
	// the mutex is locked here
	return nil
}

func ParseFrame(frame []byte) (*SmartbusMessage, error) {
	wbgo.Debug.Printf("parsing frame:\n%s", hex.Dump(frame))
	buf := bytes.NewBuffer(frame[1 : len(frame)-2]) // skip len
	var header MessageHeader
	if err := binary.Read(buf, binary.BigEndian, &header); err != nil {
		return nil, err
	}
	msgParser, found := recognizedMessages[header.Opcode]
	if !found {
		return nil, fmt.Errorf("opcode %04x not recognized", header.Opcode)
	}

	if msg, err := msgParser(buf); err != nil {
		return nil, err
	} else {
		return &SmartbusMessage{header, msg}, nil
	}
}

func ReadSmartbusFrame(reader io.Reader) ([]byte, bool) {
	var l byte
	if err := binary.Read(reader, binary.BigEndian, &l); err != nil {
		if isTimeout(reader, err) {
			wbgo.Error.Printf("timed out reading frame length")
			return nil, true
		}
		wbgo.Error.Printf("error reading frame length: %v", err)
		return nil, false
	}
	if l < MIN_FRAME_SIZE {
		wbgo.Error.Printf("frame too short")
		return nil, true
	}
	var frame []byte = make([]byte, l)
	frame[0] = l
	if _, err := io.ReadFull(reader, frame[1:]); err != nil {
		if isTimeout(reader, err) {
			wbgo.Error.Printf("timed out reading frame body")
			return nil, true
		}
		wbgo.Error.Printf("error reading frame body (%d bytes): %v", l, err)
		return nil, false
	}

	crc := crc16(frame[:len(frame)-2])
	if crc != binary.BigEndian.Uint16(frame[len(frame)-2:]) {
		wbgo.Error.Printf("bad crc (expected: 0x%02x)", crc)
		return nil, true
	}

	return frame, true
}

func ReadSmartbusRaw(reader io.Reader, mutex MutexLike, frameHandler func(frame []byte)) {
	var err error
	defer func() {
		switch {
		case err == io.EOF || err == io.ErrUnexpectedEOF:
			wbgo.Debug.Printf("eof reached")
			return
		case err == io.ErrClosedPipe:
			wbgo.Debug.Printf("pipe closed")
			return
		case err != nil:
			if !isTimeout(reader, err) {
				wbgo.Error.Printf("NOTE: connection error: %s", err)
			}
			return
		}
	}()
	for {
		if err = ReadSync(reader, mutex); err != nil {
			wbgo.Debug.Printf("ReadSync error: %v", err)
			// the mutex is not locked if ReadSync failed
			break
		}

		// the mutex is locked here
		frame, cont := ReadSmartbusFrame(reader)
		mutex.Unlock()
		if frame != nil {
			frameHandler(frame)
		}
		if !cont {
			break
		}
	}
}

func ReadSmartbus(reader io.Reader, mutex MutexLike, ch chan *SmartbusMessage, rawReadCh chan []byte) {
	ReadSmartbusRaw(reader, mutex, func(frame []byte) {
		if rawReadCh != nil {
			rawReadCh <- frame
		}
		if msg, err := ParseFrame(frame); err != nil {
			wbgo.Error.Printf("failed to parse smartbus frame: %s", err)
		} else {
			ch <- msg
		}
	})
	close(ch)
}

func WriteSmartbus(writer io.Writer, mutex MutexLike, ch chan interface{}) {
	for msg := range ch {
		mutex.Lock()
		switch msg.(type) {
		case SmartbusMessage:
			WriteFrame(writer, msg.(SmartbusMessage))
		case []byte:
			WritePreBuiltFrame(writer, msg.([]byte))
		default:
			panic("unsupported message object type")
		}
		mutex.Unlock()
	}
}

type SmartbusIO interface {
	Start() chan *SmartbusMessage
	Send(msg SmartbusMessage)
	Stop()
}

type SmartbusStreamIO struct {
	stream    io.ReadWriteCloser
	readCh    chan *SmartbusMessage
	writeCh   chan interface{}
	rawReadCh chan []byte
	mutex     sync.Mutex
}

func NewStreamIO(stream io.ReadWriteCloser, rawReadCh chan []byte) *SmartbusStreamIO {
	return &SmartbusStreamIO{
		stream:    stream,
		readCh:    make(chan *SmartbusMessage),
		writeCh:   make(chan interface{}),
		rawReadCh: rawReadCh,
	}
}

func (streamIO *SmartbusStreamIO) Start() chan *SmartbusMessage {
	go ReadSmartbus(streamIO.stream, &streamIO.mutex, streamIO.readCh, streamIO.rawReadCh)
	go WriteSmartbus(streamIO.stream, &streamIO.mutex, streamIO.writeCh)
	return streamIO.readCh
}

func (streamIO *SmartbusStreamIO) Send(msg SmartbusMessage) {
	streamIO.writeCh <- msg
}

func (streamIO *SmartbusStreamIO) SendRaw(msg []byte) {
	streamIO.writeCh <- msg
}

func (streamIO *SmartbusStreamIO) Stop() {
	close(streamIO.writeCh) // this kills WriteSmartbus goroutine
	streamIO.stream.Close() // this kills ReadSmartbus goroutine by causing read error
	for _ = range streamIO.readCh {
		// drain read queue
	}
}
