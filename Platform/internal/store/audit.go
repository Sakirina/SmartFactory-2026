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

	"competition2026/product/platform/pkg/model"
)

type AuditEvent struct {
	SourceID      string      `json:"source_id"`
	Sequence      int64       `json:"sequence"`
	OccurredMS    int64       `json:"occurred_ms"`
	ReceivedMS    int64       `json:"received_ms"`
	Actor         model.Actor `json:"actor"`
	Action        string      `json:"action"`
	Resource      string      `json:"resource"`
	RequestID     string      `json:"request_id"`
	ConfigVersion int64       `json:"config_version"`
	Snapshot      any         `json:"snapshot"`
	PreviousHash  string      `json:"previous_hash"`
	Hash          string      `json:"hash"`
}
type AuditIssue struct {
	SourceID string `json:"source_id"`
	Sequence int64  `json:"sequence"`
	Reason   string `json:"reason"`
}

func (t *Tx) Audit(actor model.Actor, action, resource, request string, snapshot any) error {
	e := AuditEvent{SourceID: t.Store.NodeID, OccurredMS: t.Store.Now().UnixMilli(), ReceivedMS: t.Store.Now().UnixMilli(), Actor: actor, Action: action, Resource: resource, RequestID: request, Snapshot: snapshot}
	err := t.QueryRowContext(t.Ctx, "SELECT sequence,hash FROM audit WHERE source_id=$1 ORDER BY sequence DESC LIMIT 1", e.SourceID).Scan(&e.Sequence, &e.PreviousHash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	e.Sequence++
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	h := sha256.Sum256(b)
	e.Hash = hex.EncodeToString(h[:])
	b, err = json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = t.ExecContext(t.Ctx, "INSERT INTO audit(source_id,sequence,occurred_ms,received_ms,request_id,previous_hash,hash,data) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", e.SourceID, e.Sequence, e.OccurredMS, e.ReceivedMS, e.RequestID, e.PreviousHash, e.Hash, string(b))
	if err != nil {
		return err
	}
	sig := ed25519.Sign(t.Store.SignKey, []byte(checkpointText(e.SourceID, e.Sequence, e.Hash)))
	pub := t.Store.SignKey.Public().(ed25519.PublicKey)
	_, err = t.ExecContext(t.Ctx, "INSERT INTO audit_checkpoints(source_id,sequence,hash,signature,public_key) VALUES($1,$2,$3,$4,$5)", e.SourceID, e.Sequence, e.Hash, base64.StdEncoding.EncodeToString(sig), base64.StdEncoding.EncodeToString(pub))
	return err
}
func checkpointText(source string, seq int64, hash string) string {
	return fmt.Sprintf("%s:%d:%s", source, seq, hash)
}
func (s *Store) Audit(ctx context.Context, actor model.Actor, action, resource, request string, snapshot any) error {
	return s.Write(ctx, func(t *Tx) error { return t.Audit(actor, action, resource, request, snapshot) })
}
func (s *Store) AuditList(ctx context.Context, request string, limit int) ([]AuditEvent, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	q := "SELECT data FROM audit"
	args := []any{}
	if request != "" {
		q += " WHERE request_id=$1"
		args = append(args, request)
	}
	q += fmt.Sprintf(" ORDER BY received_ms DESC,sequence DESC LIMIT %d", limit)
	rows, e := s.DB.QueryContext(ctx, q, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []AuditEvent{}
	for rows.Next() {
		var b string
		var v AuditEvent
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(b), &v); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) VerifyAudit(ctx context.Context) ([]AuditIssue, error) {
	trusted := map[string]string{s.NodeID: base64.StdEncoding.EncodeToString(s.SignKey.Public().(ed25519.PublicKey))}
	docs, err := s.List(ctx, "audit_trust")
	if err != nil {
		return nil, err
	}
	for _, doc := range docs {
		var v struct {
			PublicKey string `json:"public_key"`
		}
		if err = DecodeJSON(doc.Data, &v); err != nil {
			return nil, err
		}
		trusted[doc.ID] = v.PublicKey
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT source_id,sequence,previous_hash,hash,data FROM audit ORDER BY source_id,sequence")
	if err != nil {
		return nil, err
	}
	issues := []AuditIssue{}
	last := map[string]AuditEvent{}
	hashes := map[string]string{}
	for rows.Next() {
		var source, prev, hash, b string
		var seq int64
		if err = rows.Scan(&source, &seq, &prev, &hash, &b); err != nil {
			rows.Close()
			return nil, err
		}
		var e AuditEvent
		if err = json.Unmarshal([]byte(b), &e); err != nil {
			issues = append(issues, AuditIssue{source, seq, "invalid record JSON"})
			continue
		}
		p := last[source]
		if seq != p.Sequence+1 {
			issues = append(issues, AuditIssue{source, seq, "missing or reordered sequence"})
		}
		if prev != p.Hash || e.PreviousHash != prev {
			issues = append(issues, AuditIssue{source, seq, "previous hash mismatch"})
		}
		suffix := `,"hash":"` + hash + `"}`
		serialized := strings.TrimSuffix(b, suffix) + `,"hash":""}`
		h := sha256.Sum256([]byte(serialized))
		if hex.EncodeToString(h[:]) != hash || e.SourceID != source || e.Sequence != seq {
			issues = append(issues, AuditIssue{source, seq, "record altered"})
		}
		e.Hash = hash
		last[source] = e
		hashes[checkpointText(source, seq, "")] = hash
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = s.DB.QueryContext(ctx, "SELECT source_id,sequence,hash,signature,public_key FROM audit_checkpoints ORDER BY source_id,sequence")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var source, hash, sigText, pubText string
		var seq int64
		if err = rows.Scan(&source, &seq, &hash, &sigText, &pubText); err != nil {
			return nil, err
		}
		sig, e1 := base64.StdEncoding.DecodeString(sigText)
		pub, e2 := base64.StdEncoding.DecodeString(pubText)
		if e1 != nil || e2 != nil || len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, []byte(checkpointText(source, seq, hash)), sig) {
			issues = append(issues, AuditIssue{source, seq, "invalid checkpoint signature"})
		}
		if trusted[source] != pubText {
			issues = append(issues, AuditIssue{source, seq, "untrusted signing key"})
		}
		if hashes[checkpointText(source, seq, "")] != hash {
			issues = append(issues, AuditIssue{source, seq, "checkpoint record missing or changed"})
		}
	}
	return issues, rows.Err()
}
