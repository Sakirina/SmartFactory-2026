package datatransfer

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/platform/internal/control"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type Bridge struct {
	Store  *store.Store
	NodeID string
	Client dt.DataTransferServiceClient
	conn   *grpc.ClientConn
}

func Open(s *store.Store, nodeID, address string, tlsConfig *tls.Config) (*Bridge, error) {
	transport := credentials.TransportCredentials(insecure.NewCredentials())
	if tlsConfig != nil {
		transport = credentials.NewTLS(tlsConfig)
	} else {
		host, _, e := net.SplitHostPort(address)
		if e != nil || !net.ParseIP(host).IsLoopback() {
			return nil, errors.New("non-loopback DataTransfer connections require TLS")
		}
	}
	conn, e := grpc.NewClient(address, grpc.WithTransportCredentials(transport), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(8<<20)))
	if e != nil {
		return nil, e
	}
	return &Bridge{Store: s, NodeID: nodeID, Client: dt.NewDataTransferServiceClient(conn), conn: conn}, nil
}
func (b *Bridge) Close() error {
	if b.conn != nil {
		return b.conn.Close()
	}
	return nil
}

func Value(value *dt.DataValue) any {
	switch v := value.GetKind().(type) {
	case *dt.DataValue_IntValue:
		return json.Number(strconv.FormatInt(v.IntValue, 10))
	case *dt.DataValue_UintValue:
		return json.Number(strconv.FormatUint(v.UintValue, 10))
	case *dt.DataValue_DoubleValue:
		return v.DoubleValue
	case *dt.DataValue_BoolValue:
		return v.BoolValue
	case *dt.DataValue_StringValue:
		return v.StringValue
	case *dt.DataValue_BytesValue:
		return base64.StdEncoding.EncodeToString(v.BytesValue)
	default:
		return nil
	}
}
func (b *Bridge) entity(ctx context.Context, id string) (model.Entity, error) {
	doc, e := b.Store.Get(ctx, "entity", id)
	if e != nil {
		return model.Entity{}, e
	}
	entity, e := store.Decode[model.Entity](doc)
	if e == nil && (entity.Kind != "device" || entity.EdgeID != b.NodeID || entity.Status != "approved") {
		e = errors.New("device is not approved at this edge")
	}
	return entity, e
}
func (b *Bridge) Accept(ctx context.Context, message *dt.DeviceMessage) (store.IngestResult, error) {
	results, err := b.AcceptBatch(ctx, []*dt.DeviceMessage{message})
	if len(results) == 0 {
		return store.IngestResult{}, err
	}
	return results[0], err
}

func (b *Bridge) prepare(ctx context.Context, message *dt.DeviceMessage) (store.IngestBatch, []byte, error) {
	if message == nil || message.MessageId == "" || message.Device == nil {
		return store.IngestBatch{}, nil, errors.New("invalid DataTransfer message")
	}
	entity, e := b.entity(ctx, message.Device.DeviceId)
	if e != nil {
		return store.IngestBatch{}, nil, e
	}
	raw, e := proto.MarshalOptions{Deterministic: true}.Marshal(message)
	if e != nil {
		return store.IngestBatch{}, nil, e
	}
	digest := sha256.Sum256(raw)
	batch := store.IngestBatch{MessageID: message.MessageId, SourceID: b.NodeID, PayloadHash: hex.EncodeToString(digest[:]), Critical: message.Type != dt.MessageType_TELEMETRY, Points: []model.Observation{}}
	entityRevision := message.EntityRevision
	if entityRevision == 0 {
		entityRevision = entity.Version
	}
	assetVersion := int64(0)
	if entity.ParentID != "" {
		parent, e := b.Store.Get(ctx, "entity", entity.ParentID)
		if e == nil {
			assetVersion = parent.Version
		}
	}
	point := func(key string, value any, ts int64, quality, reason, unit, source string) model.Observation {
		if ts <= 0 {
			ts = message.Timestamp
		}
		if ts <= 0 {
			ts = b.Store.Now().UnixMilli()
		}
		if source == "" {
			source = message.TimeSource
		}
		if source == "" {
			source = "collector"
		}
		return model.Observation{DeviceID: entity.ID, Key: key, Value: value, ObservedMS: ts, Quality: quality, QualityReason: reason, TimeSource: source, Unit: unit, EntityRevision: entityRevision, AssetVersion: assetVersion, SourceSequence: message.SourceSequence}
	}
	if telemetry := message.GetTelemetry(); telemetry != nil {
		for _, p := range telemetry.Datapoints {
			quality := p.Quality.String()
			if p.Quality == dt.DataQuality_QUALITY_UNSPECIFIED {
				quality = "UNCERTAIN"
			}
			batch.Points = append(batch.Points, point(p.Key, Value(p.Value), p.Timestamp, quality, p.QualityReason, p.Unit, p.TimeSource))
		}
	}
	if event := message.GetEvent(); event != nil && event.EventType == "data_gap" {
		parse := func(key string) int64 { v, _ := strconv.ParseInt(event.Data[key], 10, 64); return v }
		if event.Data["scope"] == "collection_queue" || event.Data["scope"] == "persistent_buffer" || event.Data["scope"] == "subscriber:"+b.NodeID {
			batch.Gaps = []model.DataGap{{DeviceID: entity.ID, Key: event.Data["key"], FromMS: parse("from_ms"), ToMS: parse("to_ms"), Missing: parse("missing"), Scope: event.Data["scope"], Reason: event.Description}}
		}
	} else if event != nil {
		for key, raw := range event.Data {
			// Event fields are typed JSON scalars in the protocol contract.
			var value any
			if e := store.DecodeJSON([]byte(raw), &value); e != nil {
				value = raw
			}
			switch value.(type) {
			case json.Number, bool, string:
				batch.Points = append(batch.Points, point(key, value, message.Timestamp, "GOOD", "", "", "device"))
			}
		}
	}
	if batch.Critical {
		raw, e := protojson.MarshalOptions{UseProtoNames: true}.Marshal(message)
		if e != nil {
			return store.IngestBatch{}, nil, e
		}
		batch.Event = json.RawMessage(raw)
	}
	return batch, digest[:], nil
}

func (b *Bridge) AcceptBatch(ctx context.Context, messages []*dt.DeviceMessage) ([]store.IngestResult, error) {
	batches := make([]store.IngestBatch, 0, len(messages))
	digests := make([][]byte, 0, len(messages))
	for _, message := range messages {
		batch, digest, err := b.prepare(ctx, message)
		if err != nil {
			return nil, err
		}
		batches = append(batches, batch)
		digests = append(digests, digest)
	}
	results, err := b.Store.IngestMessages(ctx, batches)
	if err != nil {
		return results, err
	}
	var failures []error
	for i, batch := range batches {
		if !batch.Critical {
			continue
		}
		ack, err := b.Client.AcknowledgeMessage(ctx, &dt.MessageAcknowledgement{MessageId: batch.MessageID, PayloadSha256: digests[i], ReceiverId: b.NodeID})
		if err == nil && !ack.GetSuccess() {
			err = errors.New("DataTransfer did not accept the business acknowledgement")
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	return results, errors.Join(failures...)
}

func (b *Bridge) Send(ctx context.Context, step model.Step, id string, deadline int64) (control.DispatchResult, error) {
	if step.EdgeID != b.NodeID {
		return control.DispatchResult{}, errors.New("device commands must be sent by their owning edge")
	}
	if _, e := b.entity(ctx, step.DeviceID); e != nil {
		return control.DispatchResult{}, e
	}
	response, e := b.Client.SendCommand(ctx, &dt.DeviceMessage{
		MessageId: "command:" + id, CommandId: id, Timestamp: b.Store.Now().UnixMilli(), Direction: dt.Direction_DOWNSTREAM,
		Type: dt.MessageType_CONTROL, Device: &dt.DeviceIdentity{DeviceId: step.DeviceID},
		Payload: &dt.DeviceMessage_Control{Control: &dt.ControlPayload{Action: step.Action, Params: step.Params, Options: &dt.CommandOptions{TimeoutMs: int32(step.TimeoutMS), Idempotent: step.Idempotent, StartDeadlineMs: deadline}}},
	})
	if e != nil {
		return control.DispatchResult{}, e
	}
	if response.CommandId != id {
		return control.DispatchResult{}, errors.New("device acknowledgement command_id mismatch")
	}
	return control.DispatchResult{Status: response.Status.String(), Message: response.Message}, nil
}

func (b *Bridge) Discover(ctx context.Context) error {
	response, e := b.Client.ListDevices(ctx, &dt.ListDevicesRequest{})
	if e != nil {
		return e
	}
	for _, device := range response.Devices {
		id := device.GetIdentity().GetDeviceId()
		if id == "" {
			continue
		}
		if _, e = b.Store.Get(ctx, "entity", id); e == nil {
			continue
		} else if !errors.Is(e, store.ErrNotFound) {
			return e
		}
		entity := model.Entity{ID: id, Name: device.Identity.DeviceName, Kind: "device", EdgeID: b.NodeID, Protocol: device.Identity.Protocol, Tags: device.Identity.Tags, Status: "candidate", Version: 1}
		e = b.Store.Write(ctx, func(t *store.Tx) error {
			if _, e := t.Get("entity", id); e == nil {
				return nil
			}
			if _, e := t.Put("entity", id, 0, entity); e != nil {
				return e
			}
			return t.Audit(model.Actor{UserID: "discovery", Source: b.NodeID}, "device.discovered", id, "", entity)
		})
		if e != nil {
			return e
		}
	}
	return nil
}
func (b *Bridge) PollPending(ctx context.Context) error {
	batch, e := b.Client.PullPendingMessages(ctx, &dt.PendingRequest{Limit: 256})
	if e != nil {
		return e
	}
	if len(batch.Messages) == 0 {
		return nil
	}
	if _, err := b.AcceptBatch(ctx, batch.Messages); err == nil {
		return nil
	}
	var failures []error
	for _, message := range batch.Messages {
		if _, err := b.Accept(ctx, message); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
func (b *Bridge) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		count := 0
		for {
			call, done := context.WithTimeout(ctx, 5*time.Second)
			if count%25 == 0 {
				_ = b.Discover(call)
			}
			count++
			err := b.PollPending(call)
			done()
			if err != nil && ctx.Err() == nil {
				_ = b.Store.Write(ctx, func(t *store.Tx) error {
					return t.SetEphemeral("bridge_state", b.NodeID, map[string]any{"status": "pending", "error": err.Error(), "at_ms": b.Store.Now().UnixMilli()})
				})
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	defer wg.Wait()
	for {
		if ctx.Err() != nil {
			return nil
		}
		e := b.receiveStream(ctx)
		if e != nil && ctx.Err() == nil {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
		}
	}
}

// receiveStream coalesces independent messages for at most 20 ms. Device command
// dispatch uses its own RPC and does not wait for this telemetry batch.
func (b *Bridge) receiveStream(ctx context.Context) error {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := b.Client.SubscribeTelemetry(streamCtx, &dt.SubscribeRequest{ConsumerId: b.NodeID})
	if err != nil {
		return err
	}
	messages := make(chan *dt.DeviceMessage, 256)
	receiveError := make(chan error, 1)
	go func() {
		defer close(messages)
		for {
			message, err := stream.Recv()
			if err != nil {
				receiveError <- err
				return
			}
			select {
			case messages <- message:
			case <-streamCtx.Done():
				receiveError <- streamCtx.Err()
				return
			}
		}
	}()
	timer := time.NewTicker(20 * time.Millisecond)
	defer timer.Stop()
	batch := make([]*dt.DeviceMessage, 0, 256)
	flush := func(call context.Context) {
		if len(batch) == 0 {
			return
		}
		if _, err := b.AcceptBatch(call, batch); err != nil {
			for _, message := range batch {
				if _, err = b.Accept(call, message); err != nil {
					_ = b.Store.Write(call, func(t *store.Tx) error {
						return t.SetEphemeral("data_gap", message.MessageId, map[string]any{"device_id": message.GetDevice().GetDeviceId(), "timestamp": message.Timestamp, "reason": err.Error()})
					})
				}
			}
		}
		batch = batch[:0]
	}
	for {
		select {
		case <-ctx.Done():
			// Only already received messages are flushed during graceful shutdown.
			cancel()
			for message := range messages {
				batch = append(batch, message)
			}
			finish, done := context.WithTimeout(context.Background(), 2*time.Second)
			flush(finish)
			done()
			return ctx.Err()
		case message, ok := <-messages:
			if !ok {
				flush(ctx)
				return <-receiveError
			}
			batch = append(batch, message)
			if len(batch) >= 256 {
				flush(ctx)
			}
		case <-timer.C:
			flush(ctx)
		}
	}
}

func (b *Bridge) ApplyConfig(ctx context.Context, entity model.Entity) error {
	if entity.Kind != "device" || entity.EdgeID != b.NodeID || entity.Status != "approved" || len(entity.Config) == 0 {
		return nil
	}
	var update dt.DeviceConfigUpdate
	if e := protojson.Unmarshal(entity.Config, &update); e != nil {
		return fmt.Errorf("device config: %w", e)
	}
	update.UpdateId = fmt.Sprintf("entity:%s:%d", entity.ID, entity.Version)
	update.EntityRevision = entity.Version
	if update.GetDeviceConfig() != nil && update.GetDeviceConfig().DeviceId != entity.ID {
		return errors.New("device config identifier mismatch")
	}
	if strings.TrimSpace(update.GetDeviceConfig().GetConnectorId()) == "" {
		return errors.New("connector_id required")
	}
	response, e := b.Client.PushDeviceConfig(ctx, &update)
	if e != nil {
		return e
	}
	if !response.Success {
		return errors.New(response.ErrorMessage)
	}
	return nil
}
