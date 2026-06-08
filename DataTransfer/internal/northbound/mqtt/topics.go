package mqtt

import (
	"fmt"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
)

type Topics struct {
	GatewayID string
}

func (t Topics) Upstream(messageType dtv1.MessageType) (string, byte, error) {
	switch messageType {
	case dtv1.MessageType_TELEMETRY:
		return t.up("telemetry"), 1, nil
	case dtv1.MessageType_STATUS:
		return t.up("status"), 1, nil
	case dtv1.MessageType_EVENT:
		return t.up("event"), 1, nil
	case dtv1.MessageType_CMD_RESPONSE:
		return t.up("cmd-response"), 2, nil
	default:
		return "", 0, fmt.Errorf("unsupported upstream message type %s", messageType.String())
	}
}

func (t Topics) DownCommand() string {
	return fmt.Sprintf("dt/v1/down/%s/command", t.GatewayID)
}

func (t Topics) DownConfig() string {
	return fmt.Sprintf("dt/v1/down/%s/config", t.GatewayID)
}

func (t Topics) MetaMetrics() string {
	return fmt.Sprintf("dt/v1/meta/%s/metrics", t.GatewayID)
}

func (t Topics) MetaDevices() string {
	return fmt.Sprintf("dt/v1/meta/%s/devices", t.GatewayID)
}

func (t Topics) LWT() string {
	return fmt.Sprintf("dt/v1/meta/%s/lwt", t.GatewayID)
}

func (t Topics) up(messageType string) string {
	return fmt.Sprintf("dt/v1/up/%s/%s", t.GatewayID, messageType)
}
