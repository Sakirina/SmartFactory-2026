package store

import (
	"context"
	"database/sql"
	"fmt"
)

func (s *Store) migrateNodeConfiguration(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS sf_workload_credentials(identity_id TEXT PRIMARY KEY,generation BIGINT NOT NULL,token_hash TEXT NOT NULL UNIQUE);
CREATE TABLE IF NOT EXISTS sf_configuration_reports(identity_id TEXT NOT NULL,kind TEXT NOT NULL,configuration_id TEXT NOT NULL,instance_epoch BIGINT NOT NULL,sequence BIGINT NOT NULL,report_hash TEXT NOT NULL,data TEXT NOT NULL,PRIMARY KEY(identity_id,kind,configuration_id));`)
	if err != nil {
		return err
	}
	// Existing public connector/parameter records become immutable initial versions.
	rows, err := tx.QueryContext(ctx, "SELECT kind,id,data FROM documents WHERE kind IN ('parameter','connector_configuration') ORDER BY kind,id")
	if err != nil {
		return err
	}
	type initial struct{ kind, id, data string }
	initials := []initial{}
	for rows.Next() {
		var x initial
		if err = rows.Scan(&x.kind, &x.id, &x.data); err != nil {
			rows.Close()
			return err
		}
		initials = append(initials, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, x := range initials {
		var v struct {
			Version int64 `json:"version"`
		}
		if err = DecodeJSON([]byte(x.data), &v); err != nil {
			return err
		}
		kind := "parameter_version"
		if x.kind == "connector_configuration" {
			kind = "connector_configuration_version"
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO documents(kind,id,version,updated_ms,data) VALUES($1,$2,1,$3,$4) ON CONFLICT(kind,id) DO NOTHING", kind, x.id+":"+fmt.Sprint(v.Version), s.Now().UnixMilli(), x.data); err != nil {
			return err
		}
	}
	return nil
}
