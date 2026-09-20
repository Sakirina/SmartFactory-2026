package store

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

var ErrConflict = errors.New("version or message content conflict")
var ErrNotFound = errors.New("record not found")

type Store struct {
	DB                  *sql.DB
	Driver              string
	mu                  sync.Mutex
	NodeID              string
	SignKey             ed25519.PrivateKey
	Now                 func() time.Time
	partitions          map[string]bool
	ForwardObservations bool
	policy              atomic.Pointer[RuntimePolicy]
}
type Tx struct {
	*sql.Tx
	Store      *Store
	Ctx        context.Context
	partitions map[string]bool
}
type Document struct {
	Kind      string          `json:"kind"`
	ID        string          `json:"id"`
	Version   int64           `json:"version"`
	UpdatedMS int64           `json:"updated_ms"`
	Data      json.RawMessage `json:"data"`
}

func Open(ctx context.Context, dsn, nodeID string, master []byte) (*Store, error) {
	if len(master) != 32 {
		return nil, errors.New("master key must contain 32 bytes")
	}
	driver := "sqlite"
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		driver = "pgx"
	} else if !strings.HasPrefix(dsn, "file:") && dsn != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(dsn), 0700); err != nil {
			return nil, err
		}
		f, e := os.OpenFile(dsn, os.O_CREATE|os.O_RDWR, 0600)
		if e != nil {
			return nil, e
		}
		f.Close()
	}
	var db *sql.DB
	var err error
	if driver == "pgx" {
		config, parseErr := pgx.ParseConfig(dsn)
		if parseErr != nil {
			return nil, parseErr
		}
		// Interactive partitioned queries spend substantially longer compiling
		// generated expressions than executing them. Apply this per connection.
		config.RuntimeParams["jit"] = "off"
		db = stdlib.OpenDB(*config)
	} else {
		db, err = sql.Open(driver, dsn)
		if err != nil {
			return nil, err
		}
	}
	s := &Store{DB: db, Driver: driver, NodeID: nodeID, Now: time.Now, partitions: map[string]bool{}}
	seed := sha256.Sum256(append(append([]byte{}, master...), []byte("audit/"+nodeID)...))
	s.SignKey = ed25519.NewKeyFromSeed(seed[:])
	if driver == "sqlite" {
		db.SetMaxOpenConns(1)
		for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=10000"} {
			if _, err = db.ExecContext(ctx, q); err != nil {
				db.Close()
				return nil, err
			}
		}
		if dsn == ":memory:" || (strings.HasPrefix(dsn, "file:") && strings.Contains(dsn, "mode=memory")) {
			if _, err = db.ExecContext(ctx, "PRAGMA temp_store=MEMORY"); err != nil {
				db.Close()
				return nil, err
			}
		}
	} else {
		db.SetMaxOpenConns(12)
		db.SetMaxIdleConns(4)
	}
	if err = s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error { return s.DB.Close() }
func (s *Store) Write(ctx context.Context, fn func(*Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer t.Rollback()
	if s.Driver == "pgx" {
		if _, err = t.ExecContext(ctx, "SELECT pg_advisory_xact_lock(872190006)"); err != nil {
			return err
		}
	}
	tx := &Tx{Tx: t, Store: s, Ctx: ctx, partitions: map[string]bool{}}
	if err = fn(tx); err != nil {
		return err
	}
	if err = t.Commit(); err != nil {
		return err
	}
	for name := range tx.partitions {
		s.partitions[name] = true
	}
	return nil
}
func scanDocument(row interface{ Scan(...any) error }) (Document, error) {
	var d Document
	var data string
	err := row.Scan(&d.Kind, &d.ID, &d.Version, &d.UpdatedMS, &data)
	d.Data = json.RawMessage(data)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return d, err
}
func (s *Store) Get(ctx context.Context, kind, id string) (Document, error) {
	return scanDocument(s.DB.QueryRowContext(ctx, "SELECT kind,id,version,updated_ms,data FROM documents WHERE kind=$1 AND id=$2", kind, id))
}
func (t *Tx) Get(kind, id string) (Document, error) {
	return scanDocument(t.QueryRowContext(t.Ctx, "SELECT kind,id,version,updated_ms,data FROM documents WHERE kind=$1 AND id=$2", kind, id))
}
func (s *Store) List(ctx context.Context, kind string) ([]Document, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT kind,id,version,updated_ms,data FROM documents WHERE kind=$1 ORDER BY id", kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ds := []Document{}
	for rows.Next() {
		d, e := scanDocument(rows)
		if e != nil {
			return nil, e
		}
		ds = append(ds, d)
	}
	return ds, rows.Err()
}
func (s *Store) GetMany(ctx context.Context, kind string, ids []string) (map[string]Document, error) {
	out := map[string]Document{}
	if len(ids) == 0 {
		return out, nil
	}
	var query strings.Builder
	query.WriteString("SELECT kind,id,version,updated_ms,data FROM documents WHERE kind=$1")
	args := []any{kind}
	appendIn(&query, &args, "id", ids)
	rows, err := s.DB.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			return nil, err
		}
		out[d.ID] = d
	}
	return out, rows.Err()
}
func (t *Tx) Put(kind, id string, expected int64, value any) (Document, error) {
	if kind == "" || id == "" {
		return Document{}, errors.New("kind and id are required")
	}
	d, e := t.Get(kind, id)
	if errors.Is(e, ErrNotFound) {
		d = Document{Kind: kind, ID: id}
	} else if e != nil {
		return d, e
	}
	if expected >= 0 && d.Version != expected {
		return d, ErrConflict
	}
	data, e := json.Marshal(value)
	if e != nil {
		return d, e
	}
	d.Version++
	d.UpdatedMS = t.Store.Now().UnixMilli()
	d.Data = data
	_, e = t.ExecContext(t.Ctx, "INSERT INTO documents(kind,id,version,updated_ms,data) VALUES($1,$2,$3,$4,$5) ON CONFLICT(kind,id) DO UPDATE SET version=excluded.version,updated_ms=excluded.updated_ms,data=excluded.data", kind, id, d.Version, d.UpdatedMS, string(data))
	if e != nil {
		return d, e
	}
	_, e = t.ExecContext(t.Ctx, "INSERT INTO document_versions(kind,id,version,updated_ms,data) VALUES($1,$2,$3,$4,$5)", kind, id, d.Version, d.UpdatedMS, string(data))
	if e == nil {
		e = t.RecordChange(d)
	}
	return d, e
}
func (s *Store) Put(ctx context.Context, kind, id string, expected int64, value any) (Document, error) {
	var d Document
	err := s.Write(ctx, func(t *Tx) error { var e error; d, e = t.Put(kind, id, expected, value); return e })
	return d, err
}
func (s *Store) VersionAt(ctx context.Context, kind, id string, at int64) (Document, error) {
	return scanDocument(s.DB.QueryRowContext(ctx, "SELECT kind,id,version,updated_ms,data FROM document_versions WHERE kind=$1 AND id=$2 AND updated_ms<=$3 ORDER BY version DESC LIMIT 1", kind, id, at))
}
func (s *Store) Versions(ctx context.Context, kind, id string) ([]Document, error) {
	rows, e := s.DB.QueryContext(ctx, "SELECT kind,id,version,updated_ms,data FROM document_versions WHERE kind=$1 AND id=$2 ORDER BY version", kind, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Document{}
	for rows.Next() {
		d, e := scanDocument(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func DecodeJSON(data []byte, v any) error {
	return DecodeJSONReader(bytes.NewReader(data), v)
}
func DecodeJSONReader(reader io.Reader, v any) error {
	d := json.NewDecoder(reader)
	d.UseNumber()
	return d.Decode(v)
}
func Decode[T any](d Document) (T, error) { var v T; e := DecodeJSON(d.Data, &v); return v, e }
func Hash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (t *Tx) Inbox(id, hash, source string) (bool, error) { return t.InboxUntil(id, hash, source, 0) }
func (t *Tx) InboxUntil(id, hash, source string, expiresMS int64) (bool, error) {
	var old string
	e := t.QueryRowContext(t.Ctx, "SELECT payload_hash FROM inbox WHERE id=$1", id).Scan(&old)
	if e == nil {
		if old != hash {
			return false, ErrConflict
		}
		return true, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return false, e
	}
	_, e = t.ExecContext(t.Ctx, "INSERT INTO inbox(id,payload_hash,source_id,received_ms,expires_ms) VALUES($1,$2,$3,$4,$5)", id, hash, source, t.Store.Now().UnixMilli(), expiresMS)
	return false, e
}
func (t *Tx) Enqueue(id, kind, destination string, payload any) error {
	b, e := json.Marshal(payload)
	if e != nil {
		return e
	}
	_, e = t.ExecContext(t.Ctx, "INSERT INTO outbox(id,kind,destination,payload,created_ms,attempts,next_ms,last_error) VALUES($1,$2,$3,$4,$5,0,0,'') ON CONFLICT(id) DO NOTHING", id, kind, destination, string(b), t.Store.Now().UnixMilli())
	return e
}
func (s *Store) migrate(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS documents(kind TEXT NOT NULL,id TEXT NOT NULL,version BIGINT NOT NULL,updated_ms BIGINT NOT NULL,data TEXT NOT NULL,PRIMARY KEY(kind,id))`,
		`CREATE TABLE IF NOT EXISTS document_versions(kind TEXT NOT NULL,id TEXT NOT NULL,version BIGINT NOT NULL,updated_ms BIGINT NOT NULL,data TEXT NOT NULL,PRIMARY KEY(kind,id,version))`,
		`CREATE INDEX IF NOT EXISTS document_time ON document_versions(kind,id,updated_ms)`,
		`CREATE TABLE IF NOT EXISTS sync_changes(sequence BIGINT PRIMARY KEY,kind TEXT NOT NULL,id TEXT NOT NULL,version BIGINT NOT NULL,updated_ms BIGINT NOT NULL,data TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS inbox(id TEXT PRIMARY KEY,payload_hash TEXT NOT NULL,source_id TEXT NOT NULL,received_ms BIGINT NOT NULL,expires_ms BIGINT NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS data_gaps(id TEXT PRIMARY KEY,device_id TEXT NOT NULL,key TEXT NOT NULL,from_ms BIGINT NOT NULL,to_ms BIGINT NOT NULL,data TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS data_gaps_scope ON data_gaps(device_id,key,to_ms,from_ms)`,
		`CREATE TABLE IF NOT EXISTS outbox(id TEXT PRIMARY KEY,kind TEXT NOT NULL,destination TEXT NOT NULL,payload TEXT NOT NULL,created_ms BIGINT NOT NULL,attempts INTEGER NOT NULL,next_ms BIGINT NOT NULL,last_error TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS outbox_due ON outbox(kind,next_ms,created_ms,id)`,
		`CREATE TABLE IF NOT EXISTS audit(source_id TEXT NOT NULL,sequence BIGINT NOT NULL,occurred_ms BIGINT NOT NULL,received_ms BIGINT NOT NULL,request_id TEXT NOT NULL,previous_hash TEXT NOT NULL,hash TEXT NOT NULL,data TEXT NOT NULL,PRIMARY KEY(source_id,sequence))`,
		`CREATE TABLE IF NOT EXISTS audit_checkpoints(source_id TEXT NOT NULL,sequence BIGINT NOT NULL,hash TEXT NOT NULL,signature TEXT NOT NULL,public_key TEXT NOT NULL,PRIMARY KEY(source_id,sequence))`,
		`CREATE TABLE IF NOT EXISTS latest(device_id TEXT NOT NULL,key TEXT NOT NULL,observed_ms BIGINT NOT NULL,received_ms BIGINT NOT NULL,data TEXT NOT NULL,PRIMARY KEY(device_id,key))`,
		`CREATE TABLE IF NOT EXISTS observations(id TEXT NOT NULL,observed_ms BIGINT NOT NULL,message_id TEXT NOT NULL,device_id TEXT NOT NULL,key TEXT NOT NULL,received_ms BIGINT NOT NULL,revision BIGINT NOT NULL,quality TEXT NOT NULL,definition_id TEXT NOT NULL,data TEXT NOT NULL,PRIMARY KEY(id,observed_ms))`,
		`CREATE INDEX IF NOT EXISTS observations_query ON observations(device_id,key,observed_ms,received_ms)`,
		`CREATE INDEX IF NOT EXISTS observations_recent ON observations(observed_ms DESC,device_id,key)`,
		`CREATE INDEX IF NOT EXISTS observations_revisions ON observations(device_id,key,observed_ms,revision DESC,received_ms DESC,id DESC) WHERE definition_id<>''`,
		`CREATE TABLE IF NOT EXISTS observation_archives(id TEXT PRIMARY KEY,device_id TEXT NOT NULL,key TEXT NOT NULL,first_ms BIGINT NOT NULL,last_ms BIGINT NOT NULL,raw_first_ms BIGINT NOT NULL,raw_last_ms BIGINT NOT NULL,point_count BIGINT NOT NULL,plain_bytes BIGINT NOT NULL,sha256 TEXT NOT NULL,payload BYTEA NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS observation_archives_scope ON observation_archives(device_id,key,last_ms,first_ms)`,
		`CREATE INDEX IF NOT EXISTS observation_archives_time ON observation_archives(last_ms,first_ms)`,
		`CREATE TABLE IF NOT EXISTS rollups(device_id TEXT NOT NULL,key TEXT NOT NULL,granularity TEXT NOT NULL,bucket_ms BIGINT NOT NULL,data TEXT NOT NULL,PRIMARY KEY(device_id,key,granularity,bucket_ms))`,
		`CREATE TABLE IF NOT EXISTS action_journal(command_id TEXT PRIMARY KEY,payload_hash TEXT NOT NULL,status TEXT NOT NULL,fence BIGINT NOT NULL,data TEXT NOT NULL,updated_ms BIGINT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS engine_checkpoints(state_id TEXT NOT NULL,bucket_ms BIGINT NOT NULL,at_ms BIGINT NOT NULL,data TEXT NOT NULL,PRIMARY KEY(state_id,bucket_ms))`,
	}
	for _, q := range statements {
		if s.Driver == "pgx" && strings.HasPrefix(q, "CREATE TABLE IF NOT EXISTS observations(") {
			q += " PARTITION BY RANGE (observed_ms)"
		}
		if _, e := s.DB.ExecContext(ctx, q); e != nil {
			return fmt.Errorf("schema migration: %w", e)
		}
	}
	var expiryColumn int
	columnQuery := "SELECT count(*) FROM pragma_table_info('inbox') WHERE name='expires_ms'"
	if s.Driver == "pgx" {
		columnQuery = "SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='inbox' AND column_name='expires_ms'"
	}
	if err := s.DB.QueryRowContext(ctx, columnQuery).Scan(&expiryColumn); err != nil {
		return err
	}
	if expiryColumn == 0 {
		if _, err := s.DB.ExecContext(ctx, "ALTER TABLE inbox ADD COLUMN expires_ms BIGINT NOT NULL DEFAULT 0"); err != nil {
			return err
		}
	}
	if _, err := s.DB.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS inbox_expiry ON inbox(expires_ms) WHERE expires_ms>0"); err != nil {
		return err
	}
	return s.Write(ctx, func(t *Tx) error {
		if _, e := t.Get("installation", "sync_change_log"); e == nil {
			return nil
		} else if !errors.Is(e, ErrNotFound) {
			return e
		}
		_, e := t.ExecContext(ctx, "INSERT INTO sync_changes(sequence,kind,id,version,updated_ms,data) SELECT ROW_NUMBER() OVER (ORDER BY updated_ms,kind,id,version),kind,id,version,updated_ms,data FROM document_versions WHERE kind IN ('entity','definition','department','asset_proposal','dashboard')")
		if e != nil {
			return e
		}
		_, e = t.Put("installation", "sync_change_log", 0, map[string]any{"initialized": true})
		return e
	})
}

// EnsureDay creates only a UTC daily partition whose name is generated locally.
func (t *Tx) EnsureDay(ms int64) error {
	if t.Store.Driver != "pgx" {
		return nil
	}
	d := time.UnixMilli(ms).UTC()
	start := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
	name := "observations_" + start.Format("20060102")
	if t.Store.partitions[name] || t.partitions[name] {
		return nil
	}
	_, e := t.ExecContext(t.Ctx, fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s PARTITION OF observations FOR VALUES FROM (%d) TO (%d)", name, start.UnixMilli(), start.AddDate(0, 0, 1).UnixMilli()))
	if e == nil {
		t.partitions[name] = true
	}
	return e
}
