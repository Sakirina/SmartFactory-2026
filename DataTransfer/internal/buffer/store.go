// Package buffer 实现方案B 的 SQLite 本地缓冲(设计 4.10):
// pending→sending(带 lease)→completed 两阶段确认、FIFO 容量淘汰、TTL 清理,
// 以及缓存化的容量统计(避免热路径全表扫描)。
package buffer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	dterrors "competition2026/product/datatransfer/internal/errors"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"
)

const (
	StatusPending   = "pending"
	StatusSending   = "sending"
	StatusCompleted = "completed"
)

type Store struct {
	db      *sql.DB
	cfg     config.BufferConfig
	dropped atomic.Int64
	// usedBytes 缓存 payload 总字节数,避免每次入队全表 SUM(5000 msg/s 时为热点)。
	// 入队/清理时增量维护,清理路径会用精确 SUM 重新校准。
	usedBytes     atomic.Int64
	highWaterWarn atomic.Bool
}

type Record struct {
	ID          int64
	MessageID   string
	MessageType dtv1.MessageType
	CreatedAtMS int64
	Payload     []byte
	Message     *dtv1.DeviceMessage
}

type Stats struct {
	Pending          int64
	Sending          int64
	Completed        int64
	Dropped          int64
	Retry            int64
	LastErrorCount   int64
	CapacityBytes    int64
	UsedBytes        int64
	UsagePercent     float64
	ReplayBatchTotal int64
}

func Open(ctx context.Context, cfg config.BufferConfig) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.Path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", cfg.Path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, cfg: cfg}
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.recalculateUsedBytes(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) recalculateUsedBytes(ctx context.Context) error {
	var used int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(LENGTH(payload)), 0) FROM outbound_messages`).Scan(&used); err != nil {
		return err
	}
	s.usedBytes.Store(used)
	return nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Enqueue(ctx context.Context, msg *dtv1.DeviceMessage) (*Record, error) {
	if msg == nil {
		return nil, errors.New("buffer message is nil")
	}
	if strings.TrimSpace(msg.MessageId) == "" {
		return nil, errors.New("buffer message_id is required")
	}
	payload, err := proto.Marshal(msg)
	if err != nil {
		return nil, err
	}
	device := msg.GetDevice()
	res, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO outbound_messages (
  message_id, batch_key, message_type, device_id, connector_id,
  created_at_ms, status, payload, retry_count, lease_until_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, 0)
`, msg.GetMessageId(), batchKey(msg), int32(msg.GetType()), device.GetDeviceId(), device.GetConnectorId(),
		msg.GetTimestamp(), StatusPending, payload)
	if err != nil {
		return nil, err
	}
	if affected, err := res.RowsAffected(); err == nil && affected > 0 {
		s.usedBytes.Add(int64(len(payload)))
	}
	s.checkHighWater()
	// 仅当缓存用量超限时才进入精确清理路径,避免每条消息的全表扫描。
	if capacity := s.capacityBytes(); capacity > 0 && s.usedBytes.Load() > capacity {
		if err := s.enforceCapacity(ctx); err != nil {
			return nil, err
		}
	}
	return s.recordByMessageID(ctx, msg.GetMessageId())
}

func (s *Store) capacityBytes() int64 {
	return int64(s.cfg.MaxSizeMB) * 1024 * 1024
}

// checkHighWater 在容量使用率穿越 80% 时记录一次告警(DT-BUF-001),回落后复位。
func (s *Store) checkHighWater() {
	capacity := s.capacityBytes()
	if capacity <= 0 {
		return
	}
	usage := float64(s.usedBytes.Load()) * 100 / float64(capacity)
	if usage >= 80 {
		if s.highWaterWarn.CompareAndSwap(false, true) {
			slog.Warn("buffer capacity above high watermark",
				"code", dterrors.CodeBufferHighWater,
				"usage_percent", usage,
				"capacity_mb", s.cfg.MaxSizeMB,
			)
		}
	} else if usage < 70 {
		s.highWaterWarn.Store(false)
	}
}

func (s *Store) ClaimByMessageID(ctx context.Context, messageID string, lease time.Duration) (*Record, bool, error) {
	now := time.Now().UnixMilli()
	leaseUntil := now + lease.Milliseconds()
	res, err := s.db.ExecContext(ctx, `
UPDATE outbound_messages
SET status = ?, lease_until_ms = ?
WHERE message_id = ?
  AND (status = ? OR (status = ? AND lease_until_ms <= ?))
`, StatusSending, leaseUntil, messageID, StatusPending, StatusSending, now)
	if err != nil {
		return nil, false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	if affected == 0 {
		return nil, false, nil
	}
	record, err := s.recordByMessageID(ctx, messageID)
	return record, err == nil, err
}

func (s *Store) ClaimPending(ctx context.Context, limit int, lease time.Duration) ([]Record, error) {
	if limit <= 0 {
		limit = 1
	}
	now := time.Now().UnixMilli()
	leaseUntil := now + lease.Milliseconds()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)

	rows, err := tx.QueryContext(ctx, `
SELECT id
FROM outbound_messages
WHERE status = ? OR (status = ? AND lease_until_ms <= ?)
ORDER BY created_at_ms ASC, id ASC
LIMIT ?
`, StatusPending, StatusSending, now, limit)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `
UPDATE outbound_messages
SET status = ?, lease_until_ms = ?
WHERE id = ?
`, StatusSending, leaseUntil, id); err != nil {
			return nil, err
		}
	}
	records, err := recordsByIDs(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return records, nil
}

func (s *Store) MarkCompleted(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	query, args := batchUpdate(
		`UPDATE outbound_messages SET status = ?, completed_at_ms = ?, lease_until_ms = 0 WHERE id IN`,
		[]any{StatusCompleted, now}, ids)
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

func (s *Store) MarkFailed(ctx context.Context, ids []int64, cause error) error {
	if len(ids) == 0 {
		return nil
	}
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	query, args := batchUpdate(
		`UPDATE outbound_messages SET status = ?, lease_until_ms = 0, retry_count = retry_count + 1, last_error = ? WHERE id IN`,
		[]any{StatusPending, message}, ids)
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

func batchUpdate(prefix string, fixed []any, ids []int64) (string, []any) {
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, 0, len(fixed)+len(ids))
	args = append(args, fixed...)
	for _, id := range ids {
		args = append(args, id)
	}
	return prefix + " (" + placeholders + ")", args
}

func (s *Store) Cleanup(ctx context.Context) error {
	cutoff := time.Now().Add(-time.Duration(s.cfg.TTLHours) * time.Hour).UnixMilli()
	deletions := []struct {
		query string
		args  []any
	}{
		{
			query: `DELETE FROM outbound_messages WHERE status = ? AND COALESCE(completed_at_ms, created_at_ms) < ?`,
			args:  []any{StatusCompleted, cutoff},
		},
		{
			// pending 与 lease 早已过期的 sending 记录一并按 TTL 清理,
			// 避免续传中断后残留 sending 行永不回收。
			query: `DELETE FROM outbound_messages WHERE status IN (?, ?) AND created_at_ms < ?`,
			args:  []any{StatusPending, StatusSending, cutoff},
		},
	}
	for _, deletion := range deletions {
		res, err := s.db.ExecContext(ctx, deletion.query, deletion.args...)
		if err != nil {
			return err
		}
		if count, err := res.RowsAffected(); err == nil && count > 0 {
			s.dropped.Add(count)
		}
	}
	// 删除后用精确 SUM 校准缓存,消除增量维护的累计误差。
	if err := s.recalculateUsedBytes(ctx); err != nil {
		return err
	}
	if capacity := s.capacityBytes(); capacity > 0 && s.usedBytes.Load() > capacity {
		return s.enforceCapacity(ctx)
	}
	return nil
}

func (s *Store) Stats(ctx context.Context) (Stats, error) {
	stats := Stats{
		Dropped:       s.dropped.Load(),
		CapacityBytes: int64(s.cfg.MaxSizeMB) * 1024 * 1024,
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT status, COUNT(*), COALESCE(SUM(retry_count), 0)
FROM outbound_messages
GROUP BY status
`)
	if err != nil {
		return Stats{}, err
	}
	for rows.Next() {
		var status string
		var count int64
		var retry int64
		if err := rows.Scan(&status, &count, &retry); err != nil {
			_ = rows.Close()
			return Stats{}, err
		}
		stats.Retry += retry
		switch status {
		case StatusPending:
			stats.Pending = count
		case StatusSending:
			stats.Sending = count
		case StatusCompleted:
			stats.Completed = count
		}
	}
	if err := rows.Close(); err != nil {
		return Stats{}, err
	}
	if err := rows.Err(); err != nil {
		return Stats{}, err
	}
	stats.UsedBytes = s.usedBytes.Load()
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM outbound_messages
WHERE last_error IS NOT NULL AND last_error != ''
`).Scan(&stats.LastErrorCount); err != nil {
		return Stats{}, err
	}
	if stats.CapacityBytes > 0 {
		stats.UsagePercent = float64(stats.UsedBytes) * 100 / float64(stats.CapacityBytes)
	}
	return stats, nil
}

func (s *Store) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS outbound_messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  message_id TEXT NOT NULL,
  batch_key TEXT NOT NULL,
  message_type INTEGER NOT NULL,
  device_id TEXT NOT NULL,
  connector_id TEXT NOT NULL,
  created_at_ms INTEGER NOT NULL,
  status TEXT NOT NULL,
  payload BLOB NOT NULL,
  retry_count INTEGER NOT NULL DEFAULT 0,
  last_error TEXT,
  lease_until_ms INTEGER NOT NULL DEFAULT 0,
  completed_at_ms INTEGER
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_outbound_message_id ON outbound_messages(message_id);
CREATE INDEX IF NOT EXISTS idx_outbound_pending ON outbound_messages(status, created_at_ms);
CREATE INDEX IF NOT EXISTS idx_outbound_device ON outbound_messages(device_id, created_at_ms);
`)
	return err
}

func (s *Store) recordByMessageID(ctx context.Context, messageID string) (*Record, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, message_id, message_type, created_at_ms, payload
FROM outbound_messages
WHERE message_id = ?
`, messageID)
	return scanRecord(row)
}

func recordsByIDs(ctx context.Context, tx *sql.Tx, ids []int64) ([]Record, error) {
	records := make([]Record, 0, len(ids))
	for _, id := range ids {
		row := tx.QueryRowContext(ctx, `
SELECT id, message_id, message_type, created_at_ms, payload
FROM outbound_messages
WHERE id = ?
`, id)
		record, err := scanRecord(row)
		if err != nil {
			return nil, err
		}
		records = append(records, *record)
	}
	return records, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRecord(row scanner) (*Record, error) {
	var record Record
	var messageType int32
	if err := row.Scan(&record.ID, &record.MessageID, &messageType, &record.CreatedAtMS, &record.Payload); err != nil {
		return nil, err
	}
	record.MessageType = dtv1.MessageType(messageType)
	var msg dtv1.DeviceMessage
	if err := proto.Unmarshal(record.Payload, &msg); err != nil {
		return nil, err
	}
	record.Message = &msg
	return &record, nil
}

// enforceCapacity 按 FIFO 淘汰最旧的非 sending 记录,直至回到容量内(FR-S-023)。
// 仅在缓存用量显示超限时被调用;进入后先做一次精确校准。
func (s *Store) enforceCapacity(ctx context.Context) error {
	capacity := s.capacityBytes()
	if capacity <= 0 {
		return nil
	}
	if err := s.recalculateUsedBytes(ctx); err != nil {
		return err
	}
	evicted := false
	for s.usedBytes.Load() > capacity {
		var id, size int64
		err := s.db.QueryRowContext(ctx, `
SELECT id, LENGTH(payload)
FROM outbound_messages
WHERE status != ?
ORDER BY created_at_ms ASC, id ASC
LIMIT 1
`, StatusSending).Scan(&id, &size)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		res, err := s.db.ExecContext(ctx, `DELETE FROM outbound_messages WHERE id = ?`, id)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return nil
		}
		if !evicted {
			evicted = true
			slog.Warn("buffer is full; evicting oldest records",
				"code", dterrors.CodeBufferFull,
				"capacity_mb", s.cfg.MaxSizeMB,
			)
		}
		s.dropped.Add(affected)
		s.usedBytes.Add(-size)
	}
	return nil
}

func batchKey(msg *dtv1.DeviceMessage) string {
	return fmt.Sprintf("%s:%s", msg.GetType().String(), msg.GetDevice().GetConnectorId())
}

func rollback(tx *sql.Tx) {
	_ = tx.Rollback()
}
