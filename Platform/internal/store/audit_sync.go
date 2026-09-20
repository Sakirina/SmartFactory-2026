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
	e := s.Write(ctx, func(t *Tx) error {
		var previous string
		e := t.QueryRowContext(ctx, "SELECT sequence,hash FROM audit WHERE source_id=$1 ORDER BY sequence DESC LIMIT 1", source).Scan(&last, &previous)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		for _, x := range records {
			var a AuditEvent
			if e = DecodeJSON(x.Record, &a); e != nil {
				return e
			}
			if a.SourceID != source {
				return errors.New("audit source differs from certificate identity")
			}
			suffix := `,"hash":"` + a.Hash + `"}`
			if !strings.HasSuffix(string(x.Record), suffix) {
				return errors.New("audit serialization changed")
			}
			hash := sha256.Sum256([]byte(strings.TrimSuffix(string(x.Record), suffix) + `,"hash":""}`))
			sig, err := base64.StdEncoding.DecodeString(x.Signature)
			if err != nil || len(public) != ed25519.PublicKeySize || hex.EncodeToString(hash[:]) != a.Hash || !ed25519.Verify(public, []byte(checkpointText(source, a.Sequence, a.Hash)), sig) {
				return fmt.Errorf("untrusted audit checkpoint at sequence %d", a.Sequence)
			}
			if a.Sequence <= last {
				var existing string
				if e = t.QueryRowContext(ctx, "SELECT hash FROM audit WHERE source_id=$1 AND sequence=$2", source, a.Sequence).Scan(&existing); e != nil {
					return e
				}
				if existing != a.Hash {
					return ErrConflict
				}
				continue
			}
			if a.Sequence != last+1 || a.PreviousHash != previous {
				return fmt.Errorf("audit sequence gap after %d", last)
			}
			if _, e = t.ExecContext(ctx, "INSERT INTO audit(source_id,sequence,occurred_ms,received_ms,request_id,previous_hash,hash,data) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", source, a.Sequence, a.OccurredMS, a.ReceivedMS, a.RequestID, a.PreviousHash, a.Hash, string(x.Record)); e != nil {
				return e
			}
			if _, e = t.ExecContext(ctx, "INSERT INTO audit_checkpoints(source_id,sequence,hash,signature,public_key) VALUES($1,$2,$3,$4,$5)", source, a.Sequence, a.Hash, x.Signature, base64.StdEncoding.EncodeToString(public)); e != nil {
				return e
			}
			last, previous = a.Sequence, a.Hash
		}
		return t.SetEphemeral("audit_trust", source, map[string]string{"public_key": base64.StdEncoding.EncodeToString(public)})
	})
	return last, e
}
