// Package state persists command reservations and acknowledged configuration snapshots.
package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"
)

var ErrConflict = errors.New("identifier already exists with different content")

type Store struct{ db *sql.DB }

func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("state database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	_, err = db.ExecContext(ctx, `PRAGMA journal_mode=WAL;
PRAGMA synchronous=FULL;
PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS commands (
 command_id TEXT PRIMARY KEY, digest BLOB NOT NULL, response BLOB NOT NULL,
 phase TEXT NOT NULL, updated_at_ms INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS config_updates (
 update_id TEXT PRIMARY KEY, digest BLOB NOT NULL, response BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS telemetry_consumers(id TEXT PRIMARY KEY,data BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS config_snapshot (
 id INTEGER PRIMARY KEY CHECK(id=1), data BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS durable_outbox (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT, message_id TEXT NOT NULL UNIQUE,
 digest BLOB NOT NULL, payload BLOB NOT NULL, acknowledged INTEGER NOT NULL DEFAULT 0,
 receiver_id TEXT NOT NULL DEFAULT '', acknowledged_at_ms INTEGER
);
CREATE INDEX IF NOT EXISTS durable_outbox_pending ON durable_outbox(acknowledged,sequence);`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err = s.initGaps(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err = s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS durable_outbox_compaction ON durable_outbox(sequence) WHERE acknowledged=1 AND length(payload)>0`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if path != ":memory:" {
		if err := os.Chmod(path, 0o600); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func digest(msg proto.Message) ([]byte, error) {
	data, err := (proto.MarshalOptions{Deterministic: true}).Marshal(msg)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(data)
	return hash[:], nil
}

// Reserve commits before any device I/O. An unfinished record always returns an
// unknown outcome, including after process termination, and is never re-executed.
func (s *Store) Reserve(ctx context.Context, msg *dtv1.DeviceMessage) (*dtv1.CommandResponsePayload, bool, error) {
	hash, err := digest(msg)
	if err != nil {
		return nil, false, err
	}
	unknown := &dtv1.CommandResponsePayload{CommandId: msg.CommandId, Status: dtv1.CommandStatus_RESULT_UNKNOWN, Message: "execution is in progress or was interrupted; reconcile device state before another action"}
	data, err := proto.Marshal(unknown)
	if err != nil {
		return nil, false, err
	}
	result, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO commands(command_id,digest,response,phase,updated_at_ms) VALUES(?,?,?,'running',?)`, msg.CommandId, hash, data, time.Now().UnixMilli())
	if err != nil {
		return nil, false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	if n == 1 {
		return nil, false, nil
	}
	var storedHash, response []byte
	if err := s.db.QueryRowContext(ctx, `SELECT digest,response FROM commands WHERE command_id=?`, msg.CommandId).Scan(&storedHash, &response); err != nil {
		return nil, false, err
	}
	if !bytes.Equal(hash, storedHash) {
		return nil, false, fmt.Errorf("%w: command %s", ErrConflict, msg.CommandId)
	}
	var out dtv1.CommandResponsePayload
	if err := proto.Unmarshal(response, &out); err != nil {
		return nil, false, err
	}
	return &out, true, nil
}

func (s *Store) Complete(ctx context.Context, response *dtv1.CommandResponsePayload) error {
	data, err := proto.Marshal(response)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE commands SET response=?,phase='complete',updated_at_ms=? WHERE command_id=? AND phase='running'`, data, time.Now().UnixMilli(), response.CommandId)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("command %s has no active reservation", response.CommandId)
	}
	return nil
}

func (s *Store) Configuration(ctx context.Context, update *dtv1.DeviceConfigUpdate) (*dtv1.ConfigUpdateResponse, bool, error) {
	var storedHash, response []byte
	err := s.db.QueryRowContext(ctx, `SELECT digest,response FROM config_updates WHERE update_id=?`, update.UpdateId).Scan(&storedHash, &response)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	hash, err := digest(update)
	if err != nil {
		return nil, false, err
	}
	if !bytes.Equal(hash, storedHash) {
		return nil, false, fmt.Errorf("%w: configuration %s", ErrConflict, update.UpdateId)
	}
	var out dtv1.ConfigUpdateResponse
	if err := proto.Unmarshal(response, &out); err != nil {
		return nil, false, err
	}
	return &out, true, nil
}

// SaveConfiguration atomically records the response and the full active snapshot.
func (s *Store) SaveConfiguration(ctx context.Context, update *dtv1.DeviceConfigUpdate, response *dtv1.ConfigUpdateResponse, snapshot []byte) error {
	hash, err := digest(update)
	if err != nil {
		return err
	}
	data, err := proto.Marshal(response)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO config_updates(update_id,digest,response) VALUES(?,?,?)`, update.UpdateId, hash, data); err != nil {
		return err
	}
	if snapshot != nil {
		if _, err := tx.ExecContext(ctx, `INSERT INTO config_snapshot(id,data) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data`, snapshot); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) LoadSnapshot(ctx context.Context) ([]byte, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM config_snapshot WHERE id=1`).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return data, err
}
