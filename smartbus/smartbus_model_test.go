package smartbus

import (
	"net"
	"fmt"
	"testing"
)

func doTestSmartbusDriver(t *testing.T,
	thunk func (conn *SmartbusConnection, driver *Driver, broker *FakeMQTTBroker,
		handler *FakeHandler, client *FakeMQTTClient)) {

	p, r := net.Pipe()

	broker := NewFakeMQTTBroker(t)
	model := NewSmartbusModel(func () (SmartbusIO, error) {
		return NewStreamIO(p), nil
	}, SAMPLE_APP_SUBNET, SAMPLE_APP_DEVICE_ID, SAMPLE_APP_DEVICE_TYPE)
	client := broker.MakeClient("tst", func (msg MQTTMessage) {
		t.Logf("tst: message %v", msg)
	})
	client.Start()
	driver := NewDriver(model, func (handler MQTTMessageHandler) MQTTClient {
		return broker.MakeClient("driver", handler)
	})

	handler := NewFakeHandler(t)
	conn := NewSmartbusConnection(NewStreamIO(r))
	thunk(conn, driver, broker, handler, client)
}

func TestSmartbusDriverZoneBeastHandling(t *testing.T) {
	doTestSmartbusDriver(t, func (conn *SmartbusConnection, driver *Driver, broker *FakeMQTTBroker, handler *FakeHandler, client *FakeMQTTClient) {

		relay_ep := conn.MakeSmartbusEndpoint(
			SAMPLE_SUBNET, SAMPLE_RELAY_DEVICE_ID, SAMPLE_RELAY_DEVICE_TYPE)
		relay_ep.Observe(handler)
		relay_to_all_dev := relay_ep.GetBroadcastDevice()

		driver.Start()

		relay_to_all_dev.ZoneBeastBroadcast([]byte{ 0 }, parseChannelStatus("---x"))
		broker.Verify(
			"driver -> /devices/zonebeast011c/meta/name: [Zone Beast 01:1c] (QoS 1, retained)",

			"driver -> /devices/zonebeast011c/controls/Channel 1/meta/type: [switch] (QoS 1, retained)",
			"driver -> /devices/zonebeast011c/controls/Channel 1/meta/order: [1] (QoS 1, retained)",
			"driver -> /devices/zonebeast011c/controls/Channel 1: [0] (QoS 1, retained)",
			"Subscribe -- driver: /devices/zonebeast011c/controls/Channel 1/on",

			"driver -> /devices/zonebeast011c/controls/Channel 2/meta/type: [switch] (QoS 1, retained)",
			"driver -> /devices/zonebeast011c/controls/Channel 2/meta/order: [2] (QoS 1, retained)",
			"driver -> /devices/zonebeast011c/controls/Channel 2: [0] (QoS 1, retained)",
			"Subscribe -- driver: /devices/zonebeast011c/controls/Channel 2/on",

			"driver -> /devices/zonebeast011c/controls/Channel 3/meta/type: [switch] (QoS 1, retained)",
			"driver -> /devices/zonebeast011c/controls/Channel 3/meta/order: [3] (QoS 1, retained)",
			"driver -> /devices/zonebeast011c/controls/Channel 3: [0] (QoS 1, retained)",
			"Subscribe -- driver: /devices/zonebeast011c/controls/Channel 3/on",

			"driver -> /devices/zonebeast011c/controls/Channel 4/meta/type: [switch] (QoS 1, retained)",
			"driver -> /devices/zonebeast011c/controls/Channel 4/meta/order: [4] (QoS 1, retained)",
			"driver -> /devices/zonebeast011c/controls/Channel 4: [1] (QoS 1, retained)",
			"Subscribe -- driver: /devices/zonebeast011c/controls/Channel 4/on",
		)

		relay_to_all_dev.ZoneBeastBroadcast([]byte{ 0 }, parseChannelStatus("x---"))
		broker.Verify(
			"driver -> /devices/zonebeast011c/controls/Channel 1: [1] (QoS 1, retained)",
			"driver -> /devices/zonebeast011c/controls/Channel 4: [0] (QoS 1, retained)",
		)

		client.Publish(MQTTMessage{"/devices/zonebeast011c/controls/Channel 2/on", "1", 1, false})
		// note that SingleChannelControlResponse carries pre-command channel status
		handler.Verify("03/fe (type fffe) -> 01/1c: <SingleChannelControlCommand 2/100/0>")
		relay_to_all_dev.SingleChannelControlResponse(2, true, LIGHT_LEVEL_ON, parseChannelStatus("x---"))
		broker.Verify(
			"tst -> /devices/zonebeast011c/controls/Channel 2/on: [1] (QoS 1)",
			"driver -> /devices/zonebeast011c/controls/Channel 2: [1] (QoS 1, retained)",
		)

		client.Publish(MQTTMessage{"/devices/zonebeast011c/controls/Channel 1/on", "0", 1, false})
		handler.Verify("03/fe (type fffe) -> 01/1c: <SingleChannelControlCommand 1/0/0>")
		relay_to_all_dev.SingleChannelControlResponse(1, true, LIGHT_LEVEL_OFF, parseChannelStatus("xx--"))
		broker.Verify(
			"tst -> /devices/zonebeast011c/controls/Channel 1/on: [0] (QoS 1)",
			"driver -> /devices/zonebeast011c/controls/Channel 1: [0] (QoS 1, retained)",
		)

		// TBD: off (ch 1)

		driver.Stop()
		conn.Close()
		broker.Verify(
			"stop: driver",
		)
	})
}

func TestSmartbusDriverDDPHandling(t *testing.T) {
	doTestSmartbusDriver(t, func (conn *SmartbusConnection, driver *Driver, broker *FakeMQTTBroker, handler *FakeHandler, client *FakeMQTTClient) {
		ddp_ep := conn.MakeSmartbusEndpoint(
			SAMPLE_SUBNET, SAMPLE_DDP_DEVICE_ID, SAMPLE_DDP_DEVICE_TYPE)
		ddp_ep.Observe(handler)
		ddp_to_app_dev := ddp_ep.GetSmartbusDevice(SAMPLE_APP_SUBNET, SAMPLE_APP_DEVICE_ID)

		driver.Start()
		// FIXME: should use proper device discovery
		ddp_to_app_dev.QueryModules()
		broker.Verify(
			"driver -> /devices/ddp0114/meta/name: [DDP 01:14] (QoS 1, retained)")
		for i := 1; i <= PANEL_BUTTON_COUNT; i++ {
			handler.Verify(fmt.Sprintf(
				"03/fe (type fffe) -> 01/14: <QueryPanelButtonAssignment %d/1>", i))
			assignment := -1
			if i <= 10 {
				ddp_to_app_dev.QueryPanelButtonAssignmentResponse(
					uint8(i), 1, BUTTON_COMMAND_INVALID, 0, 0, 0, 0, 0)
			} else {
				assignment = i - 10
				ddp_to_app_dev.QueryPanelButtonAssignmentResponse(
					uint8(i), 1, BUTTON_COMMAND_SINGLE_CHANNEL_LIGHTING_CONTROL,
					SAMPLE_APP_SUBNET, SAMPLE_APP_DEVICE_ID,
					uint8(assignment), 100, 0)
			}
			path := fmt.Sprintf("/devices/ddp0114/controls/Page%dButton%d",
				(i - 1) / 4 + 1, (i - 1) % 4 + 1)
			broker.Verify(
				fmt.Sprintf("driver -> %s/meta/type: [text] (QoS 1, retained)", path),
				fmt.Sprintf("driver -> %s/meta/order: [%d] (QoS 1, retained)", path, i),
				fmt.Sprintf("driver -> %s: [%d] (QoS 1, retained)", path, assignment),
				fmt.Sprintf("Subscribe -- driver: %s/on", path),
			)
		}

		// second QueryModules shouldn't cause anything
		ddp_to_app_dev.QueryModules()
		handler.Verify()
		broker.Verify()

		client.Publish(MQTTMessage{"/devices/ddp0114/controls/Page1Button2/on", "10", 1, false})

		handler.Verify("03/fe (type fffe) -> 01/14: " +
			"<SetPanelButtonModes " +
			"1/1:Invalid,1/2:SingleOnOff,1/3:Invalid,1/4:Invalid," +
			"2/1:Invalid,2/2:Invalid,2/3:Invalid,2/4:Invalid," +
			"3/1:Invalid,3/2:Invalid,3/3:SingleOnOff,3/4:SingleOnOff," +
			"4/1:SingleOnOff,4/2:SingleOnOff,4/3:SingleOnOff,4/4:SingleOnOff>")
		ddp_to_app_dev.SetPanelButtonModesResponse(true)
		broker.Verify("tst -> /devices/ddp0114/controls/Page1Button2/on: [10] (QoS 1)")
		handler.Verify("03/fe (type fffe) -> 01/14: <AssignPanelButton 2/1/59/03/fe/10/100/0/0>")
		ddp_to_app_dev.AssignPanelButtonResponse(2, 1)
		broker.Verify("driver -> /devices/ddp0114/controls/Page1Button2: [10] (QoS 1, retained)")
	})
}

// TBD: outdated ZoneBeastBroadcast messages still arrive sometimes, need to fix this