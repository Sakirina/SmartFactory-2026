package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
)

func (s *Store) migrateQueries(ctx context.Context, tx *sql.Tx) error {
	blob, collation := "BLOB", "BINARY"
	if s.Driver == "pgx" {
		blob, collation = "BYTEA", `"C"`
	}
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`
CREATE TABLE sf_query_state(singleton INTEGER PRIMARY KEY,head BIGINT NOT NULL,floor BIGINT NOT NULL,epoch TEXT NOT NULL,auth_revision BIGINT NOT NULL);
CREATE TABLE sf_query_rows(kind TEXT NOT NULL,id TEXT COLLATE %s NOT NULL,valid_from BIGINT NOT NULL,valid_to BIGINT NOT NULL,deleted INTEGER NOT NULL,version BIGINT NOT NULL,sort_ms BIGINT NOT NULL,resource_id TEXT NOT NULL,parent_id TEXT NOT NULL,name TEXT NOT NULL,status TEXT NOT NULL,entity_kind TEXT NOT NULL,definition_id TEXT NOT NULL,metric_key TEXT NOT NULL,resolution TEXT NOT NULL,assignee_id TEXT NOT NULL,handling_status TEXT NOT NULL,active INTEGER NOT NULL,acknowledged INTEGER NOT NULL,data %s NOT NULL,PRIMARY KEY(kind,id,valid_from));
CREATE INDEX sf_query_current ON sf_query_rows(kind,valid_to,deleted,sort_ms DESC,id);
CREATE INDEX sf_query_history ON sf_query_rows(kind,resource_id,sort_ms DESC,id,valid_from,valid_to);
CREATE INDEX sf_query_series ON sf_query_rows(kind,resolution,resource_id,metric_key,sort_ms DESC,id);
CREATE INDEX sf_query_definition ON sf_query_rows(kind,definition_id,valid_to,deleted,id);
CREATE INDEX sf_query_parent ON sf_query_rows(kind,parent_id,valid_to,deleted,id);
CREATE TABLE sf_query_resources(kind TEXT NOT NULL,id TEXT NOT NULL,valid_from BIGINT NOT NULL,resource_id TEXT NOT NULL,PRIMARY KEY(kind,id,valid_from,resource_id));
CREATE INDEX sf_query_resources_candidate ON sf_query_resources(resource_id,kind,id,valid_from);
CREATE TABLE sf_query_commits(sequence BIGINT PRIMARY KEY,at_ms BIGINT NOT NULL,kinds TEXT NOT NULL);
`, collation, blob))
	if err != nil {
		return err
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO sf_query_state(singleton,head,floor,epoch,auth_revision) VALUES(1,0,0,$1,0)", hex.EncodeToString(random[:])); err != nil {
		return err
	}
	t := &Tx{Tx: tx, Store: s, Ctx: ctx}
	return t.rebuildQueryProjection()
}
