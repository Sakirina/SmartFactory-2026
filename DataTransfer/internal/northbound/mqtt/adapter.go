package mqtt

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	dterrors "competition2026/product/datatransfer/internal/errors"
	dtruntime "competition2026/product/datatransfer/internal/runtime"
	paho "github.com/eclipse/paho.mqtt.golang"
	"google.golang.org/protobuf/proto"
)

type Adapter struct {
	cfg    config.MQTTConfig
	rt     *dtruntime.Runtime
	log    *slog.Logger
	client paho.Client
	topics Topics
}

func New(cfg config.MQTTConfig, rt *dtruntime.Runtime, logger *slog.Logger) *Adapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Adapter{
		cfg:    cfg,
		rt:     rt,
		log:    logger,
		topics: Topics{GatewayID: cfg.GatewayID},
	}
}

func (a *Adapter) Start(ctx context.Context) error {
	opts := paho.NewClientOptions().
		AddBroker(a.cfg.Broker).
		SetClientID(a.cfg.ClientID).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectTimeout(time.Duration(a.cfg.ConnectTimeout) * time.Second).
		SetOrderMatters(false)

	if a.cfg.Username != "" {
		opts.SetUsername(a.cfg.Username)
	}
	if a.cfg.Password != "" {
		opts.SetPassword(a.cfg.Password)
	}
	if a.cfg.TLS.Enabled {
		opts.SetTLSConfig(&tls.Config{InsecureSkipVerify: a.cfg.TLS.InsecureSkipVerify})
	}
	opts.SetConnectionLostHandler(func(_ paho.Client, err error) {
		a.rt.SetMQTTConnected(false)
		a.log.Warn("mqtt connection lost", "error", err)
	})
	opts.SetReconnectingHandler(func(_ paho.Client, _ *paho.ClientOptions) {
		a.rt.SetMQTTConnected(false)
		a.log.Info("mqtt reconnecting")
	})
	opts.SetOnConnectHandler(func(client paho.Client) {
		a.rt.SetMQTTConnected(true)
		if err := a.subscribe(client); err != nil {
			a.log.Error("mqtt subscribe failed", "error", err)
		}
	})
	opts.SetWill(a.topics.LWT(), "offline", 1, true)

	a.client = paho.NewClient(opts)
	token := a.client.Connect()
	if !token.WaitTimeout(time.Duration(a.cfg.ConnectTimeout) * time.Second) {
		a.log.Warn("mqtt initial connect is still pending")
	} else if err := token.Error(); err != nil {
		a.log.Warn("mqtt initial connect failed; reconnect will continue", "error", err)
	}

	<-ctx.Done()
	a.rt.SetMQTTConnected(false)
	if a.client != nil && a.client.IsConnected() {
		a.client.Disconnect(250)
	}
	return nil
}

func (a *Adapter) PublishUpstream(ctx context.Context, msg *dtv1.DeviceMessage) error {
	if a.client == nil {
		return fmt.Errorf("mqtt client is not initialized")
	}
	topic, qos, err := a.topics.Upstream(msg.GetType())
	if err != nil {
		return err
	}
	return a.publishBatch(ctx, topic, qos, []*dtv1.DeviceMessage{msg})
}

func (a *Adapter) subscribe(client paho.Client) error {
	filters := map[string]byte{
		a.topics.DownCommand(): 2,
		a.topics.DownConfig():  2,
	}
	token := client.SubscribeMultiple(filters, a.routeMessage)
	token.Wait()
	return token.Error()
}

func (a *Adapter) routeMessage(_ paho.Client, message paho.Message) {
	switch message.Topic() {
	case a.topics.DownCommand():
		a.handleCommand(message.Payload())
	case a.topics.DownConfig():
		a.handleConfig(message.Payload())
	default:
		a.log.Warn("mqtt message on unexpected topic", "topic", message.Topic())
	}
}

func (a *Adapter) handleCommand(payload []byte) {
	var msg dtv1.DeviceMessage
	if err := proto.Unmarshal(payload, &msg); err != nil {
		a.log.Error("mqtt command decode failed", "code", dterrors.CodeMQTTDecodeFailed, "error", err)
		return
	}
	response, _, err := a.rt.HandleCommand(context.Background(), &msg)
	if err != nil {
		a.log.Error("mqtt command rejected", "error", err, "command_id", msg.GetCommandId())
		return
	}
	responseMsg := &dtv1.DeviceMessage{
		MessageId: fmt.Sprintf("mqtt-response-%d", time.Now().UnixNano()),
		Timestamp: time.Now().UnixMilli(),
		Direction: dtv1.Direction_UPSTREAM,
		Device:    msg.GetDevice(),
		Type:      dtv1.MessageType_CMD_RESPONSE,
		CommandId: msg.GetCommandId(),
		Payload: &dtv1.DeviceMessage_CmdResponse{
			CmdResponse: response,
		},
		Metadata: map[string]string{"phase": "P1"},
	}
	if err := a.PublishUpstream(context.Background(), responseMsg); err != nil {
		a.log.Error("mqtt command response publish failed", "error", err, "command_id", msg.GetCommandId())
	}
}

func (a *Adapter) handleConfig(payload []byte) {
	var update dtv1.DeviceConfigUpdate
	if err := proto.Unmarshal(payload, &update); err != nil {
		a.log.Error("mqtt config decode failed", "code", dterrors.CodeMQTTDecodeFailed, "error", err)
		return
	}
	response := a.rt.RejectConfig(&update)
	a.log.Info("mqtt config update rejected in P1", "update_id", response.GetUpdateId(), "error", response.GetErrorMessage())
}

func (a *Adapter) publishBatch(ctx context.Context, topic string, qos byte, messages []*dtv1.DeviceMessage) error {
	payload, err := proto.Marshal(&dtv1.DeviceMessageBatch{
		Messages:  messages,
		BatchId:   fmt.Sprintf("batch-%d", time.Now().UnixNano()),
		CreatedAt: time.Now().UnixMilli(),
	})
	if err != nil {
		return err
	}
	token := a.client.Publish(topic, qos, false, payload)
	return waitToken(ctx, token)
}

func waitToken(ctx context.Context, token paho.Token) error {
	done := make(chan struct{})
	go func() {
		token.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return token.Error()
	}
}
