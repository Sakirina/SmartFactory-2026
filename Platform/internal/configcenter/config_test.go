package configcenter

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func TestConfigVersionAfterAcknowledgementsAndMaskedSecret(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "config.db"), "config", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Service{Store: db, Identity: &identity.Manager{Store: db, Master: make([]byte, 32)}}
	p := Parameter{ID: "secret", Program: "cloud", Secret: true, Dynamic: true, Schema: map[string]any{"type": "string"}, Value: "fixture-credential"}
	one, err := s.Put(ctx, model.Actor{}, p, 0)
	if err != nil || one.Value != "********" {
		t.Fatal(one, err)
	}
	for _, node := range []string{"cloud-a", "cloud-b"} {
		if err = s.Acknowledge(ctx, node, p.ID, 1, true, ""); err != nil {
			t.Fatal(err)
		}
	}
	p.Value = "updated-fixture-credential"
	two, err := s.Put(ctx, model.Actor{}, p, 1)
	if err != nil || two.Version != 2 {
		t.Fatal(two, err)
	}
	if err = s.Acknowledge(ctx, "cloud-a", p.ID, 1, true, ""); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale acknowledgement accepted", err)
	}
	if _, err = s.Put(ctx, model.Actor{}, p, 0); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale update accepted", err)
	}
	value, err := s.Value(ctx, p.ID)
	if err != nil || value != "updated-fixture-credential" {
		t.Fatal(value, err)
	}
	doc, err := db.Get(ctx, "parameter", p.ID)
	if err != nil || strings.Contains(string(doc.Data), "fixture-credential") {
		t.Fatal("stored secret was not encrypted", err)
	}
	audit, err := db.AuditList(ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range audit {
		raw, _ := json.Marshal(event)
		if strings.Contains(string(raw), "fixture-credential") {
			t.Fatal("audit exposed secret")
		}
	}
}
func TestSchemaRejectsRequiredUnknownAndIncorrectTypes(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []string{"limit"}, "additionalProperties": false, "properties": map[string]any{"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}}
	for _, value := range []any{map[string]any{}, map[string]any{"limit": "12"}, map[string]any{"limit": 2.5}, map[string]any{"limit": 101}, map[string]any{"limit": 12, "unknown": true}} {
		if err := Validate(schema, value); err == nil {
			t.Fatalf("invalid value accepted: %#v", value)
		}
	}
	if err := Validate(schema, map[string]any{"limit": 12}); err != nil {
		t.Fatal(err)
	}
}
