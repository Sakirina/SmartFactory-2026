package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"google.golang.org/protobuf/proto"
)

func (s *Store) Enqueue(ctx context.Context, msg *dtv1.DeviceMessage) error {
	if msg.MessageId == "" {
		return fmt.Errorf("durable message_id is required")
	}
	data, err := (proto.MarshalOptions{Deterministic: true}).Marshal(msg)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(data)
	var savedHash []byte
	lookupErr := s.db.QueryRowContext(ctx, `SELECT digest FROM durable_outbox WHERE message_id=?`, msg.MessageId).Scan(&savedHash)
	if lookupErr == nil {
		if !bytes.Equal(savedHash, hash[:]) {
			return fmt.Errorf("%w: message %s", ErrConflict, msg.MessageId)
		}
		return nil
	}
	if !errors.Is(lookupErr, sql.ErrNoRows) {
		return lookupErr
	}
	result, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO durable_outbox(message_id,digest,payload) VALUES(?,?,?)`, msg.MessageId, hash[:], data)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 1 {
		return err
	}
	var existing []byte
	if err := s.db.QueryRowContext(ctx, `SELECT digest FROM durable_outbox WHERE message_id=?`, msg.MessageId).Scan(&existing); err != nil {
		return err
	}
	if !bytes.Equal(existing, hash[:]) {
		return fmt.Errorf("%w: message %s", ErrConflict, msg.MessageId)
	}
	return nil
}

func (s *Store) Pending(ctx context.Context, limit int) ([]*dtv1.DeviceMessage, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM durable_outbox WHERE acknowledged=0 ORDER BY sequence LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*dtv1.DeviceMessage
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		msg := new(dtv1.DeviceMessage)
		if err := proto.Unmarshal(data, msg); err != nil {
			return nil, err
		}
		result = append(result, msg)
	}
	return result, rows.Err()
}

func (s *Store) Acknowledge(ctx context.Context, ack *dtv1.MessageAcknowledgement) error {
	if ack == nil || ack.MessageId == "" || len(ack.PayloadSha256) != sha256.Size || ack.ReceiverId == "" {
		return fmt.Errorf("message_id, receiver_id and SHA-256 digest are required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE durable_outbox SET acknowledged=1,receiver_id=?,acknowledged_at_ms=?,payload=X'' WHERE message_id=? AND digest=? AND acknowledged=0`, ack.ReceiverId, time.Now().UnixMilli(), ack.MessageId, ack.PayloadSha256)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		var found int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM durable_outbox WHERE message_id=? AND digest=? AND acknowledged=1`, ack.MessageId, ack.PayloadSha256).Scan(&found); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("acknowledgement does not match a persisted message")
			}
			return err
		}
	}
	return nil
}

// CompactAcknowledged only removes transport payloads already confirmed by the
// receiver. The identifier and digest remain available for replay checks.
func (s *Store) CompactAcknowledged(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	result, err := s.db.ExecContext(ctx, `UPDATE durable_outbox SET payload=X'' WHERE sequence IN (SELECT sequence FROM durable_outbox WHERE acknowledged=1 AND length(payload)>0 ORDER BY sequence LIMIT ?)`, limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
