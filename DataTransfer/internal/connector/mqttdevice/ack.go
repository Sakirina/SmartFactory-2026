package mqttdevice

import (
	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"context"
	"encoding/json"
	paho5 "github.com/eclipse/paho.golang/paho"
	"time"
)

func (c *Connector) Committed(message *dt.DeviceMessage) {
	if message.GetMetadata()["mqtt_message_id"] == "" {
		return
	}
	c.mu.RLock()
	queue := c.acknowledgements
	c.mu.RUnlock()
	select {
	case queue <- message:
	default:
	}
}
func (c *Connector) publishAcknowledgements(ctx context.Context) {
	c.mu.RLock()
	queue := c.acknowledgements
	c.mu.RUnlock()
	for {
		select {
		case <-ctx.Done():
			return
		case message := <-queue:
			raw, _ := json.Marshal(map[string]any{"message_id": message.Metadata["mqtt_message_id"], "committed": true})
			topic := "devices/" + message.GetDevice().GetDeviceId() + "/ack"
			c.mu.RLock()
			v3, v5 := c.client, c.v5
			c.mu.RUnlock()
			call, cancel := context.WithTimeout(ctx, 2*time.Second)
			if v5 != nil {
				_, _ = v5.Publish(call, &paho5.Publish{Topic: topic, QoS: 1, Payload: raw})
			} else if v3 != nil {
				_ = waitToken(call, v3.Publish(topic, 1, false, raw))
			}
			cancel()
		}
	}
}
