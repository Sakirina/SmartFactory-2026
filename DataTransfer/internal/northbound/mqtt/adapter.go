package mqtt

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/buffer"
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

	store            *buffer.Store
	bufferCfg        config.BufferConfig
	connected        atomic.Bool
	replayBatchTotal atomic.Int64
	wakeReplay       chan struct{}
}

type Option func(*Adapter)

func WithBuffer(store *buffer.Store, cfg config.BufferConfig) Option {
	return func(adapter *Adapter) {
		adapter.store = store
		adapter.bufferCfg = cfg
	}
}

func New(cfg config.MQTTConfig, rt *dtruntime.Runtime, logger *slog.Logger, opts ...Option) *Adapter {
	if logger == nil {
		logger = slog.Default()
	}
	adapter := &Adapter{
		cfg:        cfg,
		rt:         rt,
		log:        logger,
		topics:     Topics{GatewayID: cfg.GatewayID},
		wakeReplay: make(chan struct{}, 1),
		bufferCfg: config.BufferConfig{
			ResumeRateLimit:        1000,
			ResumeBatchSize:        100,
			CleanupIntervalSeconds: 60,
		},
	}
	for _, opt := range opts {
		opt(adapter)
	}
	return adapter
}

func (a *Adapter) Start(ctx context.Context) error {
	if a.store != nil {
		go a.replayLoop(ctx)
		go a.cleanupLoop(ctx)
	}
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
		a.connected.Store(false)
		a.rt.SetMQTTConnected(false)
		a.log.Warn("mqtt connection lost", "error", err)
	})
	opts.SetReconnectingHandler(func(_ paho.Client, _ *paho.ClientOptions) {
		a.connected.Store(false)
		a.rt.SetMQTTConnected(false)
		a.log.Info("mqtt reconnecting")
	})
	opts.SetOnConnectHandler(func(client paho.Client) {
		a.connected.Store(true)
		a.rt.SetMQTTConnected(true)
		if err := a.subscribe(client); err != nil {
			a.log.Error("mqtt subscribe failed", "error", err)
		}
		a.notifyReplay()
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
	a.connected.Store(false)
	a.rt.SetMQTTConnected(false)
	if a.client != nil && a.client.IsConnected() {
		a.client.Disconnect(250)
	}
	return nil
}

func (a *Adapter) HandleUpstream(ctx context.Context, msg *dtv1.DeviceMessage) error {
	return a.PublishUpstream(ctx, msg)
}

func (a *Adapter) PublishUpstream(ctx context.Context, msg *dtv1.DeviceMessage) error {
	if a.store != nil {
		return a.publishReliable(ctx, msg)
	}
	return a.publishDirect(ctx, msg)
}

func (a *Adapter) publishDirect(ctx context.Context, msg *dtv1.DeviceMessage) error {
	if a.client == nil {
		return fmt.Errorf("mqtt client is not initialized")
	}
	if !a.IsConnected() {
		return fmt.Errorf("mqtt client is not connected")
	}
	topic, qos, err := a.topics.Upstream(msg.GetType())
	if err != nil {
		return err
	}
	return a.publishBatch(ctx, topic, qos, []*dtv1.DeviceMessage{msg})
}

func (a *Adapter) publishReliable(ctx context.Context, msg *dtv1.DeviceMessage) error {
	record, err := a.store.Enqueue(ctx, msg)
	if err != nil {
		return err
	}
	if !a.IsConnected() {
		return nil
	}
	claimed, ok, err := a.store.ClaimByMessageID(ctx, record.MessageID, 30*time.Second)
	if err != nil || !ok {
		return err
	}
	if err := a.sendRecords(ctx, []buffer.Record{*claimed}); err != nil {
		_ = a.store.MarkFailed(ctx, []int64{claimed.ID}, err)
		return nil
	}
	return a.store.MarkCompleted(ctx, []int64{claimed.ID})
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

func (a *Adapter) IsConnected() bool {
	return a.connected.Load() && a.client != nil && a.client.IsConnected()
}

func (a *Adapter) BufferSnapshot() dtruntime.PersistentBufferSnapshot {
	if a.store == nil {
		return dtruntime.PersistentBufferSnapshot{}
	}
	stats, err := a.store.Stats(context.Background())
	if err != nil {
		a.log.Warn("buffer stats failed", "error", err)
		return dtruntime.PersistentBufferSnapshot{}
	}
	return dtruntime.PersistentBufferSnapshot{
		Pending:          stats.Pending,
		Sending:          stats.Sending,
		Completed:        stats.Completed,
		Dropped:          stats.Dropped,
		Retry:            stats.Retry,
		LastErrorCount:   stats.LastErrorCount,
		CapacityBytes:    stats.CapacityBytes,
		UsedBytes:        stats.UsedBytes,
		UsagePercent:     stats.UsagePercent,
		ReplayBatchTotal: a.replayBatchTotal.Load(),
	}
}

func (a *Adapter) replayLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-a.wakeReplay:
		}
		for ctx.Err() == nil && a.IsConnected() {
			records, err := a.store.ClaimPending(ctx, a.bufferCfg.ResumeBatchSize, 30*time.Second)
			if err != nil {
				a.log.Error("buffer claim failed", "error", err)
				break
			}
			if len(records) == 0 {
				break
			}
			ids := recordIDs(records)
			if err := a.sendRecords(ctx, records); err != nil {
				_ = a.store.MarkFailed(ctx, ids, err)
				a.log.Warn("buffer replay publish failed", "error", err, "records", len(records))
				break
			}
			if err := a.store.MarkCompleted(ctx, ids); err != nil {
				a.log.Error("buffer mark completed failed", "error", err, "records", len(records))
				break
			}
			a.replayBatchTotal.Add(1)
			a.applyRateLimit(ctx, len(records))
		}
	}
}

func (a *Adapter) cleanupLoop(ctx context.Context) {
	interval := time.Duration(a.bufferCfg.CleanupIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.store.Cleanup(ctx); err != nil {
				a.log.Warn("buffer cleanup failed", "error", err)
			}
		}
	}
}

func (a *Adapter) sendRecords(ctx context.Context, records []buffer.Record) error {
	grouped := make(map[dtv1.MessageType][]*dtv1.DeviceMessage)
	for _, record := range records {
		if record.Message == nil {
			continue
		}
		grouped[record.MessageType] = append(grouped[record.MessageType], record.Message)
	}
	for messageType, messages := range grouped {
		topic, qos, err := a.topics.Upstream(messageType)
		if err != nil {
			return err
		}
		if err := a.publishBatch(ctx, topic, qos, messages); err != nil {
			return err
		}
	}
	return nil
}

func (a *Adapter) publishBatch(ctx context.Context, topic string, qos byte, messages []*dtv1.DeviceMessage) error {
	if a.client == nil {
		return fmt.Errorf("mqtt client is not initialized")
	}
	if !a.IsConnected() {
		return fmt.Errorf("mqtt client is not connected")
	}
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

func (a *Adapter) notifyReplay() {
	select {
	case a.wakeReplay <- struct{}{}:
	default:
	}
}

func (a *Adapter) applyRateLimit(ctx context.Context, count int) {
	if a.bufferCfg.ResumeRateLimit <= 0 || count <= 0 {
		return
	}
	delay := time.Duration(count) * time.Second / time.Duration(a.bufferCfg.ResumeRateLimit)
	if delay <= 0 {
		return
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func recordIDs(records []buffer.Record) []int64 {
	ids := make([]int64, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.ID)
	}
	return ids
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
