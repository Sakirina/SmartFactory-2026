package buffer

import (
	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"google.golang.org/protobuf/proto"
	"strconv"
	time "time"
)

func recordGap(ctx context.Context, tx *sql.Tx, payload []byte, reason string) error {
	msg := new(dt.DeviceMessage)
	if err := proto.Unmarshal(payload, msg); err != nil {
		return err
	}
	if msg.Type != dt.MessageType_TELEMETRY {
		return fmt.Errorf("only telemetry may be evicted before acknowledgement")
	}
	hash := sha256.Sum256(payload)
	for i, p := range msg.GetTelemetry().GetDatapoints() {
		at := p.Timestamp
		if at <= 0 {
			at = msg.Timestamp
		}
		if at <= 0 {
			at = time.Now().UnixMilli()
		}
		gap := &dt.DeviceMessage{MessageId: fmt.Sprintf("buffer-gap:%x:%d", hash, i), Timestamp: at, Direction: dt.Direction_UPSTREAM, Device: msg.Device, Type: dt.MessageType_EVENT, TimeSource: "collector", Payload: &dt.DeviceMessage_Event{Event: &dt.EventPayload{EventType: "data_gap", Description: reason, Data: map[string]string{"key": p.Key, "from_ms": strconv.FormatInt(at, 10), "to_ms": strconv.FormatInt(at, 10), "missing": "1", "scope": "persistent_buffer", "reason": reason}}}}
		raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(gap)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO buffer_gaps(message_id,payload) VALUES(?,?)`, gap.MessageId, raw); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) PendingGaps(ctx context.Context) ([]*dt.DeviceMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM buffer_gaps ORDER BY message_id LIMIT 1000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var gaps []*dt.DeviceMessage
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		m := new(dt.DeviceMessage)
		if err = proto.Unmarshal(raw, m); err != nil {
			return nil, err
		}
		gaps = append(gaps, m)
	}
	return gaps, rows.Err()
}
func (s *Store) GapForwarded(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM buffer_gaps WHERE message_id=?`, id)
	return err
}

func (s *Store) expireTelemetry(ctx context.Context, cutoff int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id,payload FROM outbound_messages WHERE status IN (?,?) AND message_type=? AND created_at_ms<?`, StatusPending, StatusSending, dt.MessageType_TELEMETRY, cutoff)
	if err != nil {
		return err
	}
	type entry struct {
		id      int64
		payload []byte
	}
	var records []entry
	for rows.Next() {
		var r entry
		if err = rows.Scan(&r.id, &r.payload); err != nil {
			rows.Close()
			return err
		}
		records = append(records, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range records {
		if err = recordGap(ctx, tx, r.payload, "continuous buffer retention expired"); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM outbound_messages WHERE id=?`, r.id); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err == nil {
		s.dropped.Add(int64(len(records)))
	}
	return err
}
