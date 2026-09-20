package state

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"google.golang.org/protobuf/proto"
)

func (s *Store) initGaps(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS journal_identity(id INTEGER PRIMARY KEY CHECK(id=1), value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS continuous_gaps(id INTEGER PRIMARY KEY AUTOINCREMENT, bucket TEXT NOT NULL UNIQUE, identity BLOB NOT NULL, datapoint_key TEXT NOT NULL, from_ms INTEGER NOT NULL, to_ms INTEGER NOT NULL, missing INTEGER NOT NULL, scope TEXT NOT NULL, reason TEXT NOT NULL, created_ms INTEGER NOT NULL, last_updated_ms INTEGER NOT NULL);`)
	if err != nil {
		return err
	}
	var id [16]byte
	if _, err = rand.Read(id[:]); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO journal_identity(id,value) VALUES(1,?)`, hex.EncodeToString(id[:]))
	return err
}

// RecordGap groups losses by device, point, consumer and collection minute.
// A flushed group is immutable; subsequent losses receive another sequence.
func (s *Store) RecordGap(ctx context.Context, msg *dt.DeviceMessage, scope, reason string) error {
	return s.RecordGaps(ctx, []*dt.DeviceMessage{msg}, scope, reason)
}

// RecordGaps groups a drained queue before one durable transaction.
func (s *Store) RecordGaps(ctx context.Context, messages []*dt.DeviceMessage, scope, reason string) error {
	type pending struct {
		identity          []byte
		key               string
		from, to, missing int64
	}
	groups := map[string]*pending{}
	now := time.Now().UnixMilli()
	for _, msg := range messages {
		if msg.GetType() != dt.MessageType_TELEMETRY || msg.GetDevice().GetDeviceId() == "" {
			return fmt.Errorf("gap requires telemetry and device identity")
		}
		identity, err := proto.Marshal(msg.Device)
		if err != nil {
			return err
		}
		for _, point := range msg.GetTelemetry().GetDatapoints() {
			at := point.Timestamp
			if at <= 0 {
				at = msg.Timestamp
			}
			if at <= 0 {
				at = now
			}
			bucket := fmt.Sprintf("%q:%q:%q:%q:%d", msg.Device.DeviceId, point.Key, scope, reason, now/60000)
			g := groups[bucket]
			if g == nil {
				g = &pending{identity: identity, key: point.Key, from: at, to: at}
				groups[bucket] = g
			}
			g.from = min(g.from, at)
			g.to = max(g.to, at)
			g.missing++
		}
	}
	if len(groups) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for bucket, g := range groups {
		_, err = tx.ExecContext(ctx, `INSERT INTO continuous_gaps(bucket,identity,datapoint_key,from_ms,to_ms,missing,scope,reason,created_ms,last_updated_ms) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(bucket) DO UPDATE SET from_ms=MIN(from_ms,excluded.from_ms),to_ms=MAX(to_ms,excluded.to_ms),missing=missing+excluded.missing,last_updated_ms=excluded.last_updated_ms`, bucket, g.identity, g.key, g.from, g.to, g.missing, scope, reason, now, now)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// FlushGaps commits immutable gap events and removal of their pending groups in
// one transaction. Receiver acknowledgement uses the ordinary durable outbox.
func (s *Store) FlushGaps(ctx context.Context, force bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var namespace string
	if err = tx.QueryRowContext(ctx, `SELECT value FROM journal_identity WHERE id=1`).Scan(&namespace); err != nil {
		return err
	}
	cutoff := time.Now().Add(-time.Second).UnixMilli()
	if force {
		cutoff = time.Now().Add(time.Hour).UnixMilli()
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,identity,datapoint_key,from_ms,to_ms,missing,scope,reason FROM continuous_gaps WHERE last_updated_ms<=? OR created_ms<=? ORDER BY id LIMIT 1000`, cutoff, time.Now().Add(-time.Minute).UnixMilli())
	if err != nil {
		return err
	}
	type group struct {
		id, from, to, missing int64
		identity              []byte
		key, scope, reason    string
	}
	var groups []group
	for rows.Next() {
		var g group
		if err = rows.Scan(&g.id, &g.identity, &g.key, &g.from, &g.to, &g.missing, &g.scope, &g.reason); err != nil {
			rows.Close()
			return err
		}
		groups = append(groups, g)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, g := range groups {
		device := new(dt.DeviceIdentity)
		if err = proto.Unmarshal(g.identity, device); err != nil {
			return err
		}
		msg := &dt.DeviceMessage{MessageId: fmt.Sprintf("gap:%s:%d", namespace, g.id), Timestamp: g.to, Direction: dt.Direction_UPSTREAM, Type: dt.MessageType_EVENT, Device: device, TimeSource: "collector", Payload: &dt.DeviceMessage_Event{Event: &dt.EventPayload{EventType: "data_gap", Description: g.reason, Data: map[string]string{"key": g.key, "from_ms": strconv.FormatInt(g.from, 10), "to_ms": strconv.FormatInt(g.to, 10), "missing": strconv.FormatInt(g.missing, 10), "scope": g.scope, "reason": g.reason}}}}
		data, e := proto.MarshalOptions{Deterministic: true}.Marshal(msg)
		if e != nil {
			return e
		}
		hash, e := digest(msg)
		if e != nil {
			return e
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO durable_outbox(message_id,digest,payload) VALUES(?,?,?)`, msg.MessageId, hash, data); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM continuous_gaps WHERE id=?`, g.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
