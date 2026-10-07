package store

import (
	"context"
	"database/sql"
	"errors"
)

// migrateSyncHistory retains the legacy adoption marker and existing sequences.
func (s *Store) migrateSyncHistory(ctx context.Context, transaction *sql.Tx) error {
	tx := &Tx{Tx: transaction, Store: s, Ctx: ctx, partitions: map[string]bool{}}
	if _, err := tx.Get("installation", "sync_change_log"); err == nil {
		return nil
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	// A partially upgraded installation can already have sync rows. Preserve
	// their sequence IDs and append only document versions missing from the log.
	_, err := tx.ExecContext(ctx, `INSERT INTO sync_changes(sequence,kind,id,version,updated_ms,data)
SELECT (SELECT COALESCE(MAX(sequence),0) FROM sync_changes)+ROW_NUMBER() OVER (ORDER BY v.updated_ms,v.kind,v.id,v.version),v.kind,v.id,v.version,v.updated_ms,v.data
FROM document_versions v
WHERE v.kind IN ('entity','definition','department','asset_proposal','dashboard')
AND NOT EXISTS (SELECT 1 FROM sync_changes c WHERE c.kind=v.kind AND c.id=v.id AND c.version=v.version)`)
	if err != nil {
		return err
	}
	_, err = tx.Put("installation", "sync_change_log", 0, map[string]any{"initialized": true})
	return err
}
