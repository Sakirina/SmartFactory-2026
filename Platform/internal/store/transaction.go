package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// ErrRetryable marks a database concurrency failure without replaying a callback.
var ErrRetryable = fmt.Errorf("%w: concurrent transaction must be retried", ErrConflict)

func transactionError(err error) error {
	var postgres *pgconn.PgError
	if errors.As(err, &postgres) && (postgres.Code == "40001" || postgres.Code == "40P01") {
		return fmt.Errorf("%w (PostgreSQL %s)", ErrRetryable, postgres.Code)
	}
	return err
}

func lockKey(namespace, key string) int64 {
	hash := sha256.Sum256([]byte("smartfactory/" + namespace + "\x00" + key))
	return int64(binary.BigEndian.Uint64(hash[:8]))
}

func (t *Tx) lock(namespace, key string, shared bool) error {
	// Numbered startup migrations construct Tx directly and retain their SQL
	// behavior. Runtime transactions always set managed in Store.Write.
	if !t.managed || t.Store.Driver != "pgx" {
		return nil
	}
	name := namespace + "\x00" + key
	if previous, ok := t.locks[name]; ok {
		if previous && !shared && namespace == "collection" {
			return fmt.Errorf("%w: collection lock must precede document access", ErrRetryable)
		}
		if !previous || shared {
			return nil
		}
	}
	query := "SELECT pg_advisory_xact_lock($1)"
	if shared {
		query = "SELECT pg_advisory_xact_lock_shared($1)"
	}
	if _, err := t.ExecContext(t.Ctx, query, lockKey(namespace, key)); err != nil {
		return err
	}
	if t.locks == nil {
		t.locks = make(map[string]bool)
	}
	t.locks[name] = shared
	return nil
}

// LockCollection protects a membership/version check against new members.
// Call it for sorted kinds before reading any document of those kinds.
func (t *Tx) LockCollection(kind string) error { return t.lock("collection", kind, false) }

func (t *Tx) retirePartition(name string) error {
	if err := t.lock("partition", name, false); err != nil {
		return err
	}
	if t.retiredPartitions == nil {
		t.retiredPartitions = make(map[string]bool)
	}
	t.retiredPartitions[name] = true
	return nil
}

func (t *Tx) lockDocument(kind, id string) error {
	if err := t.lock("collection", kind, true); err != nil {
		return err
	}
	return t.lock("document", kind+"\x00"+id, false)
}

// Read holds a shared object lock, suitable for authorization and dependency
// guards. Get retains exclusive read-modify-write behavior.
func (t *Tx) Read(kind, id string) (Document, error) {
	if err := t.lock("collection", kind, true); err != nil {
		return Document{}, err
	}
	if err := t.lock("document", kind+"\x00"+id, true); err != nil {
		return Document{}, err
	}
	return scanDocument(t.QueryRowContext(t.Ctx, "SELECT kind,id,version,updated_ms,data FROM documents WHERE kind=$1 AND id=$2", kind, id))
}

// ReadCollection shares membership and existing-object locks with other
// snapshot readers. Insertion/deletion takes membership exclusive; updates
// take their own object exclusive, preserving independent-object concurrency.
func (t *Tx) ReadCollection(kind string) ([]Document, error) {
	if err := t.lock("collection", kind, true); err != nil {
		return nil, err
	}
	if err := t.lock("membership", kind, true); err != nil {
		return nil, err
	}
	rows, err := t.QueryContext(t.Ctx, "SELECT id FROM documents WHERE kind=$1 ORDER BY id", kind)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := make([]Document, 0, len(ids))
	for _, id := range ids {
		d, err := t.Read(kind, id)
		if err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, nil
}

func (t *Tx) lockInsertion(kind, id string) error {
	var exists int
	err := t.QueryRowContext(t.Ctx, "SELECT 1 FROM documents WHERE kind=$1 AND id=$2", kind, id).Scan(&exists)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return t.lock("membership", kind, false)
}

// Delete and DeleteCollection use the same locks as document updates.
func (t *Tx) Delete(kind, id string) error {
	if err := t.lock("collection", kind, true); err != nil {
		return err
	}
	if err := t.lock("membership", kind, false); err != nil {
		return err
	}
	if err := t.lockDocument(kind, id); err != nil {
		return err
	}
	_, err := t.ExecContext(t.Ctx, "DELETE FROM documents WHERE kind=$1 AND id=$2", kind, id)
	if err == nil {
		t.markQueryDocument(kind, id)
	}
	return err
}

func (t *Tx) DeleteCollection(kind string) error {
	if err := t.LockCollection(kind); err != nil {
		return err
	}
	documents, err := t.List(kind)
	if err != nil {
		return err
	}
	_, err = t.ExecContext(t.Ctx, "DELETE FROM documents WHERE kind=$1", kind)
	if err == nil {
		for _, d := range documents {
			t.markQueryDocument(kind, d.ID)
		}
	}
	return err
}

// List returns a transactionally protected complete document collection.
func (t *Tx) List(kind string) ([]Document, error) {
	if err := t.LockCollection(kind); err != nil {
		return nil, err
	}
	rows, err := t.QueryContext(t.Ctx, "SELECT kind,id,version,updated_ms,data FROM documents WHERE kind=$1 ORDER BY id", kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Document{}
	for rows.Next() {
		document, err := scanDocument(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, document)
	}
	return result, rows.Err()
}

// finalize appends logs after all business/River writes. Its lock is held until
// Commit, making sequence order match externally visible commits.
func (t *Tx) finalize() error {
	if len(t.changes) == 0 && len(t.auditFinalizers) == 0 && !t.hasQueryChanges() {
		return nil
	}
	if t.Store.Driver == "pgx" {
		if _, err := t.ExecContext(t.Ctx, "SELECT pg_advisory_xact_lock(872190006)"); err != nil {
			return err
		}
	}
	if len(t.changes) > 0 {
		var sequence int64
		if err := t.QueryRowContext(t.Ctx, "SELECT COALESCE(MAX(sequence),0) FROM sync_changes").Scan(&sequence); err != nil {
			return err
		}
		for _, document := range t.changes {
			sequence++
			if _, err := t.ExecContext(t.Ctx, "INSERT INTO sync_changes(sequence,kind,id,version,updated_ms,data) VALUES($1,$2,$3,$4,$5,$6)", sequence, document.Kind, document.ID, document.Version, document.UpdatedMS, string(document.Data)); err != nil {
				return err
			}
		}
	}
	if err := t.finalizeQueries(); err != nil {
		return err
	}
	for _, appendAudit := range t.auditFinalizers {
		if err := appendAudit(); err != nil {
			return err
		}
	}
	return nil
}
