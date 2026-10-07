package store

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type AuditTransfer struct {
	Record    json.RawMessage `json:"record"`
	Signature string          `json:"signature"`
}

func (s *Store) ExportAudit(ctx context.Context, after int64, limit int) ([]AuditTransfer, error) {
	if limit <= 0 || limit > 1000 {
		limit = 250
	}
	rows, e := s.DB.QueryContext(ctx, "SELECT a.data,c.signature FROM audit a JOIN audit_checkpoints c ON a.source_id=c.source_id AND a.sequence=c.sequence WHERE a.source_id=$1 AND a.sequence>$2 ORDER BY a.sequence LIMIT $3", s.NodeID, after, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []AuditTransfer{}
	for rows.Next() {
		var x AuditTransfer
		var raw string
		if e = rows.Scan(&raw, &x.Signature); e != nil {
			return nil, e
		}
		x.Record = json.RawMessage(raw)
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *Store) ImportAudit(ctx context.Context, source string, public ed25519.PublicKey, records []AuditTransfer) (int64, error) {
	var last int64
	if len(records) == 0 {
		err := s.DB.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0) FROM audit WHERE source_id=$1", source).Scan(&last)
		return last, err
	}
	verified := make([]AuditEvent, len(records))
	copied := make([]AuditTransfer, len(records))
	for index, record := range records {
		record.Record = append([]byte(nil), record.Record...)
		copied[index] = record
		var event AuditEvent
		if err := DecodeJSON(record.Record, &event); err != nil {
			return 0, err
		}
		if event.SourceID != source {
			return 0, errors.New("audit source differs from certificate identity")
		}
		suffix := `,"hash":"` + event.Hash + `"}`
		if !strings.HasSuffix(string(record.Record), suffix) {
			return 0, errors.New("audit serialization changed")
		}
		hash := sha256.Sum256([]byte(strings.TrimSuffix(string(record.Record), suffix) + `,"hash":""}`))
		signature, err := base64.StdEncoding.DecodeString(record.Signature)
		if err != nil || len(public) != ed25519.PublicKeySize || hex.EncodeToString(hash[:]) != event.Hash || !ed25519.Verify(public, []byte(checkpointText(source, event.Sequence, event.Hash)), signature) {
			return 0, fmt.Errorf("untrusted audit checkpoint at sequence %d", event.Sequence)
		}
		verified[index] = event
	}
	public = append(ed25519.PublicKey(nil), public...)
	err := s.Write(ctx, func(t *Tx) error {
		if err := t.SetEphemeral("audit_trust", source, map[string]string{"public_key": base64.StdEncoding.EncodeToString(public)}); err != nil {
			return err
		}
		t.auditFinalizers = append(t.auditFinalizers, func() error {
			var previous string
			err := t.QueryRowContext(ctx, "SELECT sequence,hash FROM audit WHERE source_id=$1 ORDER BY sequence DESC LIMIT 1", source).Scan(&last, &previous)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			for index, event := range verified {
				if event.Sequence <= last {
					var existing string
					if err := t.QueryRowContext(ctx, "SELECT hash FROM audit WHERE source_id=$1 AND sequence=$2", source, event.Sequence).Scan(&existing); err != nil {
						return err
					}
					if existing != event.Hash {
						return ErrConflict
					}
					continue
				}
				if event.Sequence != last+1 || event.PreviousHash != previous {
					return fmt.Errorf("audit sequence gap after %d", last)
				}
				if _, err := t.ExecContext(ctx, "INSERT INTO audit(source_id,sequence,occurred_ms,received_ms,request_id,previous_hash,hash,data) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", source, event.Sequence, event.OccurredMS, event.ReceivedMS, event.RequestID, event.PreviousHash, event.Hash, string(copied[index].Record)); err != nil {
					return err
				}
				if _, err := t.ExecContext(ctx, "INSERT INTO audit_checkpoints(source_id,sequence,hash,signature,public_key) VALUES($1,$2,$3,$4,$5)", source, event.Sequence, event.Hash, copied[index].Signature, base64.StdEncoding.EncodeToString(public)); err != nil {
					return err
				}
				last, previous = event.Sequence, event.Hash
			}
			return nil
		})
		return nil
	})
	return last, err
}
