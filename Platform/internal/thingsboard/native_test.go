package thingsboard

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func TestNativeCalculatedFieldAlarmAndRetention(t *testing.T) {
	base := os.Getenv("SF_NATIVE_URL")
	credentials := os.Getenv("SF_NATIVE_CREDENTIALS")
	if base == "" || credentials == "" {
		t.Skip("set SF_NATIVE_URL and SF_NATIVE_CREDENTIALS for the pinned native integration")
	}
	raw, err := os.ReadFile(credentials)
	if err != nil {
		t.Fatal(err)
	}
	var auth struct{ Username, Password string }
	if err = json.Unmarshal(raw, &auth); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "native.db"), "acceptance", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := &Adapter{Store: s, Client: &Client{URL: base, Username: auth.Username, Password: auth.Password}, CallbackURL: "http://127.0.0.1:1", ServiceToken: "test-only"}
	id := fmt.Sprintf("native-acceptance-%d", time.Now().UnixNano())
	entity := model.Entity{ID: id, Kind: "device", Name: id, Version: 1, Status: "approved"}
	if _, err = s.Put(ctx, "entity", id, 0, entity); err != nil {
		t.Fatal(err)
	}
	mapped, err := a.EnsureEntity(ctx, entity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if docs, e := s.List(cleanup, "tb_rule_chain"); e == nil {
			for _, doc := range docs {
				m, _ := store.Decode[ruleMapping](doc)
				_ = a.Client.Do(cleanup, "DELETE", "/api/ruleChain/"+m.ID.ID, nil, nil)
			}
		}
		// The backing database is closed by the test body; native entity cleanup uses stable ids.
		_ = a.Client.Do(cleanup, "DELETE", "/api/device/"+mapped.Native.ID, nil, nil)
	})
	definition := model.Definition{ID: id, Name: id, Kind: "analysis", Version: 1, SchemaVersion: "1.0", Status: "published", Selector: model.Selector{DeviceIDs: []string{id}, Keys: []string{"value"}, WindowMS: 60000}, Nodes: []model.Node{{ID: "in", Type: "input"}, {ID: "avg", Type: "aggregate", Params: map[string]any{"function": "avg"}}, {ID: "out", Type: "output"}}, Connections: []model.Connection{{From: "in", To: "avg"}, {From: "avg", To: "out"}}, Outputs: []model.Output{{Key: "average", Type: "number", NodeID: "out"}}}
	if err = a.Definition(ctx, definition); err != nil {
		t.Fatal(err)
	}
	chainDoc, err := s.Get(ctx, "tb_rule_chain", id)
	if err != nil {
		t.Fatal(err)
	}
	chain, _ := store.Decode[ruleMapping](chainDoc)
	assetDoc, _ := s.Get(ctx, "tb_mapping", "definition:"+id)
	asset, _ := store.Decode[Mapping](assetDoc)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = a.Client.Do(cleanup, "DELETE", "/api/ruleChain/"+chain.ID.ID, nil, nil)
		_ = a.Client.Do(cleanup, "DELETE", "/api/asset/"+asset.Native.ID, nil, nil)
	})
	telemetryPath := "/api/plugins/telemetry/DEVICE/" + mapped.Native.ID
	now := time.Now().UnixMilli()
	if err = a.Client.Do(ctx, "POST", telemetryPath+"/timeseries/ANY", []any{
		map[string]any{"ts": now, "values": telemetryFields(model.Observation{ID: "null-fixture", Key: "missing", Quality: "BAD", QualityReason: "BadSensorFailure"})},
		map[string]any{"ts": now, "values": telemetryFields(model.Observation{ID: "unsigned-fixture", Key: "unsigned", Quality: "GOOD", Value: json.Number("18446744073709551615")})},
	}, nil); err != nil {
		t.Fatal(err)
	}
	points := []any{}
	for i, value := range []int{2, 4, 8} {
		points = append(points, map[string]any{"ts": now - int64(2-i)*1000, "values": map[string]any{"sf_good.value": value}})
	}
	if err = a.Client.Do(ctx, "POST", telemetryPath+"/timeseries/ANY", points, nil); err != nil {
		t.Fatal(err)
	}
	output := "sf_native." + id + ".average"
	var calculated map[string][]struct {
		TS    int64  `json:"ts"`
		Value string `json:"value"`
	}
	deadline := time.Now().Add(25 * time.Second)
	for {
		if err = a.Client.Do(ctx, "GET", telemetryPath+"/values/timeseries?keys="+output, nil, &calculated); err != nil {
			t.Fatal(err)
		}
		if len(calculated[output]) > 0 {
			var value float64
			if _, err = fmt.Sscan(calculated[output][0].Value, &value); err == nil && math.Abs(value-14.0/3) < 1e-6 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("native calculated field mismatch: %+v", calculated)
		}
		time.Sleep(200 * time.Millisecond)
	}
	for version := int64(1); version <= 2; version++ {
		alarm := model.Alarm{ID: id, DefinitionID: id, EntityID: id, Severity: "CRITICAL", Version: version, Active: version == 1, Acknowledged: version == 2, StartedMS: now, UpdatedMS: now + version, Count: 1}
		if version == 2 {
			alarm.ClearedMS = now + 2
		}
		if err = a.Alarm(ctx, alarm); err != nil {
			t.Fatal(err)
		}
		if err = a.Alarm(ctx, alarm); err != nil {
			t.Fatal(err)
		}
		var result struct {
			Data []struct {
				Type         string `json:"type"`
				Acknowledged bool   `json:"acknowledged"`
				Cleared      bool   `json:"cleared"`
			}
		}
		if err = a.Client.Do(ctx, "GET", fmt.Sprintf("/api/alarm/DEVICE/%s?pageSize=100&page=0", mapped.Native.ID), nil, &result); err != nil {
			t.Fatal(err)
		}
		found := 0
		for _, native := range result.Data {
			if strings.HasSuffix(native.Type, ":"+id) {
				found++
				if native.Cleared != (version == 2) || native.Acknowledged != (version == 2) {
					t.Fatalf("native alarm transition: %+v", native)
				}
			}
		}
		if found != 1 {
			t.Fatalf("native alarm duplicate count: %d", found)
		}
	}
	old := now - int64(31*24*time.Hour/time.Millisecond)
	if err = a.Client.Do(ctx, "POST", telemetryPath+"/timeseries/ANY", []any{map[string]any{"ts": old, "values": map[string]any{"retention_probe": 1}}, map[string]any{"ts": now, "values": map[string]any{"retention_probe": 2}}}, nil); err != nil {
		t.Fatal(err)
	}
	if err = a.PruneTelemetry(ctx); err != nil {
		t.Fatal(err)
	}
	var retained map[string][]struct {
		Value string `json:"value"`
	}
	if err = a.Client.Do(ctx, "GET", fmt.Sprintf("%s/values/timeseries?keys=retention_probe&startTs=0&endTs=%d&agg=NONE&limit=100", telemetryPath, now+1000), nil, &retained); err != nil {
		t.Fatal(err)
	}
	if len(retained["retention_probe"]) != 1 || retained["retention_probe"][0].Value != "2" {
		t.Fatalf("native retention: %+v", retained)
	}
}
