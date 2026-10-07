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
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"competition2026/product/platform/internal/observability"
	"competition2026/product/platform/pkg/model"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/riverqueue/river"
	_ "modernc.org/sqlite"
)

var ErrConflict = errors.New("version or message content conflict")
var ErrNotFound = errors.New("record not found")

type Store struct {
	ingestSlots         chan struct{}
	controlSlots        chan struct{}
	TaskClient          *river.Client[*sql.Tx]
	DB                  *sql.DB
	Driver              string
	mu                  sync.RWMutex
	NodeID              string
	SignKey             ed25519.PrivateKey
	Now                 func() time.Time
	partitions          map[string]bool
	ForwardObservations bool
	policy              atomic.Pointer[RuntimePolicy]
}
type Tx struct {
	*sql.Tx
	Store             *Store
	Ctx               context.Context
	partitions        map[string]bool
	retiredPartitions map[string]bool
	managed           bool
	locks             map[string]bool
	changes           []Document
	auditFinalizers   []func() error
	queryDocuments    map[string][2]string
	queryPoints       map[string]queryRecord
	queryRemoved      map[string][2]string
	queryAuth         bool
	queryRebuild      bool
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
		sqliteDSN := dsn
		separator := "?"
		if strings.Contains(sqliteDSN, "?") {
			separator = "&"
		}
		sqliteDSN += separator + "_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)"
		db, err = sql.Open(driver, sqliteDSN)
		if err != nil {
			return nil, err
		}
	}
	s := &Store{ingestSlots: make(chan struct{}, 4), controlSlots: make(chan struct{}, 3), DB: db, Driver: driver, NodeID: nodeID, Now: time.Now, partitions: map[string]bool{}}
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
	s.TaskClient, err = river.NewClient(s.RiverDriver(), &river.Config{SkipUnknownJobCheck: true, PollOnly: true})
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error { return s.DB.Close() }
func (s *Store) Write(ctx context.Context, fn func(*Tx) error) (writeErr error) {
	ctx, finish := observability.StartOperation(ctx, "store.transaction", observability.Identity{})
	defer func() { writeErr = transactionError(writeErr); finish(writeErr) }()
	isolation := sql.LevelSerializable
	if s.Driver == "pgx" {
		isolation = sql.LevelReadCommitted
	}
	t, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: isolation})
	if err != nil {
		return err
	}
	defer t.Rollback()
	tx := &Tx{Tx: t, Store: s, Ctx: ctx, managed: true, partitions: map[string]bool{}}
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.finalize(); err != nil {
		return err
	}
	if err = t.Commit(); err != nil {
		return err
	}
	if len(tx.partitions) > 0 || len(tx.retiredPartitions) > 0 {
		s.mu.Lock()
		for name := range tx.partitions {
			s.partitions[name] = true
		}
		for name := range tx.retiredPartitions {
			delete(s.partitions, name)
		}
		s.mu.Unlock()
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
	if err := t.lockDocument(kind, id); err != nil {
		return Document{}, err
	}
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

// nextDocumentVersion runs with the document's exclusive lock already held.
// Deleting or restoring the current row can leave higher retained revisions.
// Imported versions keep their source numbering in ImportDocument.
func (t *Tx) nextDocumentVersion(kind, id string, current int64) (int64, error) {
	var history int64
	if err := t.QueryRowContext(t.Ctx, "SELECT COALESCE(MAX(version),0) FROM document_versions WHERE kind=$1 AND id=$2", kind, id).Scan(&history); err != nil {
		return 0, err
	}
	version := max(current, history, 0)
	if version == math.MaxInt64 {
		return 0, fmt.Errorf("%w: document revision exhausted for %s/%s", ErrConflict, kind, id)
	}
	return version + 1, nil
}

func (t *Tx) Put(kind, id string, expected int64, value any) (Document, error) {
	if kind == "" || id == "" {
		return Document{}, errors.New("kind and id are required")
	}
	d, e := t.Get(kind, id)
	if errors.Is(e, ErrNotFound) {
		if e = t.lock("membership", kind, false); e != nil {
			return Document{}, e
		}
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
	d.Version, e = t.nextDocumentVersion(kind, id, d.Version)
	if e != nil {
		return d, e
	}
	d.UpdatedMS = t.Store.Now().UnixMilli()
	d.Data = data
	if kind == "job" && t.Store.TaskClient != nil {
		var job model.Job
		if err := DecodeJSON(data, &job); err != nil {
			return d, err
		}
		if job.Kind == "recompute" && job.Status == "pending" {
			job.TaskID = fmt.Sprintf("recompute:%s:%d", id, d.Version)
			job.ReplayID, job.ReplayStartMS, job.ReplayEndMS = "", 0, 0
			job.ReplayPhase = ""
			job.CursorMS, job.Progress = 0, 0
			data, e = json.Marshal(job)
			if e != nil {
				return d, e
			}
			d.Data = data
		}
	}
	_, e = t.ExecContext(t.Ctx, "INSERT INTO documents(kind,id,version,updated_ms,data) VALUES($1,$2,$3,$4,$5) ON CONFLICT(kind,id) DO UPDATE SET version=excluded.version,updated_ms=excluded.updated_ms,data=excluded.data", kind, id, d.Version, d.UpdatedMS, string(data))
	if e != nil {
		return d, e
	}
	_, e = t.ExecContext(t.Ctx, "INSERT INTO document_versions(kind,id,version,updated_ms,data) VALUES($1,$2,$3,$4,$5)", kind, id, d.Version, d.UpdatedMS, string(data))
	if e == nil {
		e = t.RecordChange(d)
	}
	if e == nil && kind == "job" && t.Store.TaskClient != nil {
		e = t.enqueueRecompute(d)
	}
	if e == nil {
		t.markQueryDocument(kind, id)
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
	if err := t.lock("inbox", id, false); err != nil {
		return false, err
	}
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
	if e == nil && t.Store.TaskClient != nil {
		e = t.enqueueProjection(id, kind, destination, b)
	}
	return e
}

// EnsureDay creates only a UTC daily partition whose name is generated locally.
func (t *Tx) EnsureDay(ms int64) error {
	if t.Store.Driver != "pgx" {
		return nil
	}
	d := time.UnixMilli(ms).UTC()
	start := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
	name := "observations_" + start.Format("20060102")
	if t.partitions[name] {
		return nil
	}
	if err := t.lock("partition", name, true); err != nil {
		return err
	}
	var exists bool
	if err := t.QueryRowContext(t.Ctx, "SELECT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relname=$1)", name).Scan(&exists); err != nil {
		return err
	}
	if exists {
		if t.partitions == nil {
			t.partitions = make(map[string]bool)
		}
		t.partitions[name] = true
		return nil
	}
	if err := t.lock("partition", name, false); err != nil {
		return err
	}
	_, e := t.ExecContext(t.Ctx, fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s PARTITION OF observations FOR VALUES FROM (%d) TO (%d)", name, start.UnixMilli(), start.AddDate(0, 0, 1).UnixMilli()))
	if e == nil {
		if t.partitions == nil {
			t.partitions = make(map[string]bool)
		}
		t.partitions[name] = true
	}
	return e
}
