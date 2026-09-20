package thingsboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func TestProjectionSelectsNativeInputsAndBoundsCallbackBatches(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:", "cloud", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for kind, value := range map[string]any{
		"entity":     model.Entity{ID: "sensor", Kind: "device", Version: 1},
		"tb_mapping": Mapping{BusinessID: "sensor", Native: EntityID{ID: "native-sensor", EntityType: "DEVICE"}, EntityVersion: 1},
	} {
		if _, err = s.Put(ctx, kind, "sensor", 0, value); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"temperature", "humidity"} {
		d := model.Definition{ID: key, Kind: "analysis", Status: "published", Selector: model.Selector{DeviceIDs: []string{"sensor"}, Keys: []string{key}}}
		if _, err = s.Put(ctx, "definition", key, 0, d); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string][]string{}
	projected := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/auth/login":
			fmt.Fprint(w, `{"token":"fixture"}`)
		case strings.Contains(r.URL.Path, "/timeseries/"):
			var telemetry []json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&telemetry); err != nil {
				t.Error(err)
			}
			projected += len(telemetry)
			fmt.Fprint(w, `{}`)
		case strings.HasPrefix(r.URL.Path, "/api/rule-engine/"):
			var batch struct {
				Points  []model.Observation `json:"sf_observations"`
				Targets []string            `json:"sf_definition_ids"`
			}
			if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
				t.Error(err)
			}
			if len(batch.Targets) != 1 || len(batch.Points) == 0 || len(batch.Points) > 64 {
				t.Errorf("unbounded or shared callback: %+v", batch)
				return
			}
			for _, point := range batch.Points {
				if point.Key != batch.Targets[0] || point.DefinitionID == batch.Targets[0] {
					t.Errorf("unselected input: %+v", point)
				}
				seen[batch.Targets[0]] = append(seen[batch.Targets[0]], point.ID)
			}
			fmt.Fprint(w, `{"committed":true}`)
		default:
			t.Errorf("unexpected native request: %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	points := []model.Observation{}
	for i := 0; i < 130; i++ {
		for _, key := range []string{"temperature", "humidity", "actuator"} {
			points = append(points, model.Observation{ID: fmt.Sprintf("%s-%d", key, i), DeviceID: "sensor", Key: key, Value: i, Quality: "GOOD", ObservedMS: int64(i + 1)})
		}
	}
	points = append(points, model.Observation{ID: "own-output", DeviceID: "sensor", Key: "temperature", DefinitionID: "temperature", Value: 10, Quality: "GOOD", ObservedMS: 1})
	a := Adapter{Store: s, Client: &Client{URL: server.URL}}
	if err = a.Project(ctx, points); err != nil {
		t.Fatal(err)
	}
	if projected != len(points) {
		t.Fatalf("telemetry omitted: %d/%d", projected, len(points))
	}
	for _, key := range []string{"temperature", "humidity"} {
		if len(seen[key]) != 130 {
			t.Fatalf("selected points: %s=%d", key, len(seen[key]))
		}
		for i, id := range seen[key] {
			if id != fmt.Sprintf("%s-%d", key, i) {
				t.Fatalf("input order changed: %s", id)
			}
		}
	}
}
