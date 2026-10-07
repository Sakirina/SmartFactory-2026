package ai_test

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"competition2026/product/platform/internal/app"
	"competition2026/product/platform/internal/configcenter"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

const legacyModelSchema = `{"type":"object","required":["endpoint","model","api_key","timeout_ms"],"additionalProperties":false,"properties":{"endpoint":{"type":"string","maxLength":2048},"model":{"type":"string","maxLength":200},"api_key":{"type":"string","maxLength":16384},"timeout_ms":{"type":"integer","minimum":1000,"maximum":300000}}}`

type legacyConfigFixture struct {
	options   app.Options
	parameter configcenter.Parameter
	plain     any
	receipt   string
	data      string
}

func oldAIConfigDatabase(t *testing.T, custom bool) legacyConfigFixture {
	t.Helper()
	dir := t.TempDir()
	options := app.Options{Mode: "config", NodeID: "config-upgrade", DSN: filepath.Join(dir, "legacy.db"), KeyFile: filepath.Join(dir, "key"), BootstrapPassword: "test-password-1234"}
	key := make([]byte, 32)
	if err := os.WriteFile(options.KeyFile, []byte(base64.StdEncoding.EncodeToString(key)), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", options.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ddl, err := os.ReadFile("testdata/legacy-store.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(ddl)); err != nil {
		t.Fatal(err)
	}
	p := configcenter.Parameter{ID: "ai.model", Program: "customer-cloud-runtime", Category: "assistant", Description: "既有模型参数", Dynamic: true, Secret: true, Version: 7, State: "failed", Effective: map[string]int64{"cloud-a": 7, "cloud-b": 6}, Applications: map[string]configcenter.Application{"cloud-a": {Version: 7, State: "applied", AtMS: 123}, "cloud-b": {Version: 7, State: "failed", Reason: "old consumer failed", AtMS: 124}}}
	if err = store.DecodeJSON([]byte(legacyModelSchema), &p.Schema); err != nil {
		t.Fatal(err)
	}
	if custom {
		p.Schema["properties"].(map[string]any)["endpoint"].(map[string]any)["maxLength"] = 1024
	}
	plain := map[string]any{"endpoint": "http://127.0.0.1:9/v1", "model": "legacy-model", "api_key": "legacy-fixture-secret", "timeout_ms": 60000}
	encoded, _ := json.Marshal(plain)
	cipher, err := (&identity.Manager{Master: key}).Encrypt("config:ai.model", string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	p.Value = map[string]any{"ciphertext": cipher}
	raw, _ := json.Marshal(p)
	prior := p
	prior.Version = 6
	previous, _ := json.Marshal(prior)
	semantic := p
	semantic.Value, semantic.Effective, semantic.Applications, semantic.State = plain, nil, nil, ""
	receipt, _ := json.Marshal(map[string]any{"hash": store.Hash(semantic)})
	public := p
	public.Value = "********"
	publicRaw, _ := json.Marshal(public)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{"INSERT INTO documents VALUES('parameter','ai.model',12,200,$1)", []any{string(raw)}},
		{"INSERT INTO document_versions VALUES('parameter','ai.model',11,100,$1)", []any{string(previous)}},
		{"INSERT INTO document_versions VALUES('parameter','ai.model',12,200,$1)", []any{string(raw)}},
		{"INSERT INTO documents VALUES('parameter_receipt','ai.model:7',3,125,$1)", []any{string(receipt)}},
		{"INSERT INTO documents VALUES('user','legacy-admin',1,99,$1)", []any{`{"id":"legacy-admin","active":true,"roles":["admin"]}`}},
		{"INSERT INTO documents VALUES('entity','legacy-entity',4,99,$1)", []any{`{"id":"legacy-entity","counter":9007199254740993}`}},
		{"INSERT INTO inbox VALUES('legacy-message','legacy-hash','edge-old',100,0)", nil},
		{"INSERT INTO latest VALUES('legacy-device','count',100,101,$1)", []any{`{"id":"old-point","value":9223372036854775807}`}},
		{"INSERT INTO observations VALUES('old-point',100,'legacy-message','legacy-device','count',101,1,'GOOD','',$1)", []any{`{"id":"old-point","value":9223372036854775807}`}},
		{"INSERT INTO outbox VALUES('config:ai.model:7','config_update','customer-cloud-runtime',$1,201,2,205,'old retry')", []any{string(publicRaw)}},
		{"INSERT INTO audit VALUES('legacy-source',4,100,101,'request-old','prior-hash','old-hash',$1)", []any{`{"source_id":"legacy-source","sequence":4,"action":"config.update","snapshot":{"value":"********"}}`}},
	} {
		if _, err = db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	return legacyConfigFixture{options: options, parameter: p, plain: plain, receipt: string(receipt), data: string(raw)}
}

func configParameter(t *testing.T, db *store.Store) (store.Document, configcenter.Parameter) {
	t.Helper()
	doc, err := db.Get(context.Background(), "parameter", "ai.model")
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.Decode[configcenter.Parameter](doc)
	if err != nil {
		t.Fatal(err)
	}
	return doc, p
}

func TestLegacyAIModelStartupUpgradeRetainsEncryptedStateAndHistory(t *testing.T) {
	ctx := context.Background()
	fixture := oldAIConfigDatabase(t, false)
	for attempt := 0; attempt < 2; attempt++ {
		a, err := app.Open(ctx, fixture.options)
		if err != nil {
			t.Fatal(err)
		}
		doc, p := configParameter(t, a.Store)
		if doc.Version != 13 || p.Version != 8 || p.State != "pending" || p.Program != fixture.parameter.Program || store.Hash(p.Value) != store.Hash(fixture.parameter.Value) || store.Hash(p.Effective) != store.Hash(fixture.parameter.Effective) || store.Hash(p.Applications) != store.Hash(fixture.parameter.Applications) {
			t.Fatal("upgrade changed retained configuration state")
		}
		value, err := a.Server.Config.Value(ctx, "ai.model")
		if err != nil || store.Hash(value) != store.Hash(fixture.plain) {
			t.Fatal("legacy model value changed", err)
		}
		configured := map[string]any{"provider": "openai", "api": "responses", "stream": false, "endpoint": "http://127.0.0.1:9/v1", "model": "fixture", "api_key": "fixture-new-secret", "timeout_ms": 5000}
		if err = configcenter.Validate(p.Schema, configured); err != nil {
			t.Fatal(err)
		}
		receipt, err := a.Store.Get(ctx, "parameter_receipt", "ai.model:7")
		if err != nil || receipt.Version != 3 || string(receipt.Data) != fixture.receipt {
			t.Fatal("legacy receipt changed", err)
		}
		history, err := a.Store.Versions(ctx, "parameter", "ai.model")
		if err != nil || len(history) != 3 || string(history[1].Data) != fixture.data {
			t.Fatal("legacy history changed", err)
		}
		for _, query := range []string{"SELECT data FROM observations WHERE id='old-point'", "SELECT data FROM latest WHERE device_id='legacy-device'"} {
			var data string
			if err = a.Store.DB.QueryRowContext(ctx, query).Scan(&data); err != nil || data != `{"id":"old-point","value":9223372036854775807}` {
				t.Fatal("legacy precise observation changed", err)
			}
		}
		deliveries, err := a.Store.Deliveries(ctx, "config_update", 100)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, delivery := range deliveries {
			if delivery.ID == "config:ai.model:8" {
				count++
				if delivery.Destination != p.Program || strings.Contains(string(delivery.Payload), "fixture-secret") || strings.Contains(string(delivery.Payload), "ciphertext") {
					t.Fatal("upgrade delivery contains private value")
				}
			}
		}
		if count != 1 {
			t.Fatal("missing or duplicated upgrade delivery")
		}
		var audits int
		if err = a.Store.DB.QueryRowContext(ctx, "SELECT count(*) FROM audit WHERE data LIKE '%config.schema_upgrade%'").Scan(&audits); err != nil || audits != 1 {
			t.Fatal("upgrade audit missing or repeated", err)
		}
		if attempt == 1 {
			p.Value = configured
			updated, err := a.Server.Config.Put(ctx, model.Actor{UserID: "legacy-admin"}, p, p.Version)
			if err != nil || updated.Version != 9 {
				t.Fatal("new API fields cannot be saved after upgrade", err)
			}
			if err = a.Server.Config.Acknowledge(ctx, "cloud-a", "ai.model", 7, true, ""); !errors.Is(err, store.ErrConflict) {
				t.Fatal("stale application acknowledgement accepted", err)
			}
		}
		a.Close()
	}
}

func TestLegacyAIModelCustomSchemaAndSubscriberDoNotUpgrade(t *testing.T) {
	for _, name := range []string{"custom schema", "subscribed node"} {
		t.Run(name, func(t *testing.T) {
			fixture := oldAIConfigDatabase(t, name == "custom schema")
			if name == "subscribed node" {
				fixture.options.Mode = "cloud"
				fixture.options.ConfigURL = "http://127.0.0.1:9"
			}
			a, err := app.Open(context.Background(), fixture.options)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			doc, p := configParameter(t, a.Store)
			if doc.Version != 12 || p.Version != 7 || string(doc.Data) != fixture.data {
				t.Fatal("unrelated schema or subscribed revision upgraded locally")
			}
		})
	}
}

func TestLegacyAIModelUpgradeFailureRollsBackConfigurationAuditAndOutbox(t *testing.T) {
	fixture := oldAIConfigDatabase(t, false)
	db, err := sql.Open("sqlite", fixture.options.DSN)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TRIGGER fail_ai_upgrade BEFORE INSERT ON audit WHEN json_extract(NEW.data,'$.action')='config.schema_upgrade' BEGIN SELECT RAISE(ABORT,'fixture audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if a, err := app.Open(context.Background(), fixture.options); err == nil {
		a.Close()
		t.Fatal("audit failure accepted")
	}
	db, err = sql.Open("sqlite", fixture.options.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var data string
	var version, count int64
	if err = db.QueryRow("SELECT version,data FROM documents WHERE kind='parameter' AND id='ai.model'").Scan(&version, &data); err != nil || version != 12 || data != fixture.data {
		t.Fatal("failed upgrade changed active revision", err)
	}
	for _, query := range []string{"SELECT count(*) FROM document_versions WHERE kind='parameter' AND id='ai.model' AND version=13", "SELECT count(*) FROM outbox WHERE id='config:ai.model:8'", "SELECT count(*) FROM audit WHERE data LIKE '%config.schema_upgrade%'"} {
		if err = db.QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatal("failed upgrade committed a partial result", err)
		}
	}
}

func TestLegacyAIModelUpgradeSubscriptionsKeepImmutableReceipts(t *testing.T) {
	ctx := context.Background()
	fixture := oldAIConfigDatabase(t, false)
	remote, err := store.Open(ctx, fixture.options.DSN, fixture.options.NodeID, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	localFixture := oldAIConfigDatabase(t, false)
	local, err := store.Open(ctx, localFixture.options.DSN, "subscribed-cloud", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	service := &configcenter.Service{Store: remote, Identity: &identity.Manager{Store: remote, Master: make([]byte, 32)}}
	consumer := &configcenter.Service{Store: local, Identity: &identity.Manager{Store: local, Master: make([]byte, 32)}}
	mux := http.NewServeMux()
	service.RegisterInternal(mux, "fixture-service-token")
	host := httptest.NewServer(mux)
	defer host.Close()
	subscriber := &configcenter.Subscriber{URL: host.URL, Token: "fixture-service-token", NodeID: "cloud-a", Local: consumer}
	old := fixture.parameter
	old.Value = fixture.plain
	if err = subscriber.Apply(ctx, []configcenter.Parameter{old}); err != nil {
		t.Fatal(err)
	}
	if err = service.UpgradeModelSchema(ctx); err != nil {
		t.Fatal(err)
	}
	_, updated := configParameter(t, remote)
	updated.Value = fixture.plain
	if err = subscriber.Apply(ctx, []configcenter.Parameter{updated}); err != nil {
		t.Fatal(err)
	}
	_, received := configParameter(t, local)
	if received.Version != 8 || received.Applications["cloud-a"].Version != 8 || received.Applications["cloud-a"].State != "applied" {
		t.Fatal("new schema revision was not applied")
	}
	prior, err := local.Get(ctx, "parameter_receipt", "ai.model:7")
	if err != nil || string(prior.Data) != fixture.receipt {
		t.Fatal("old receipt was rewritten", err)
	}
	if _, err = local.Get(ctx, "parameter_receipt", "ai.model:8"); err != nil {
		t.Fatal("new version has no receipt", err)
	}
	restarted := &configcenter.Subscriber{URL: host.URL, Token: "fixture-service-token", NodeID: "cloud-a", Local: consumer}
	if err = restarted.Apply(ctx, []configcenter.Parameter{updated}); err != nil {
		t.Fatal(err)
	}
	updated.Schema = old.Schema
	if err = restarted.Apply(ctx, []configcenter.Parameter{updated}); err != nil {
		t.Fatal(err)
	}
	_, retained := configParameter(t, local)
	if store.Hash(retained.Schema) != store.Hash(received.Schema) {
		t.Fatal("same-version schema conflict replaced accepted value")
	}
}
