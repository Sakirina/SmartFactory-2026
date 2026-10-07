package cloudsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/compatibility"
	"competition2026/product/platform/pkg/model"
)

func TestIncompatibleExchangeDoesNotAdvanceCursorOrImportData(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	peer := compatibility.CurrentPeer("edge")
	peer.Protocols["sync"] = "v2"
	entity := model.Entity{ID: "future-device", Kind: "device", EdgeID: "edge-a", Version: 1}
	raw, _ := json.Marshal(entity)
	request := Request{Compatibility: peer, Changes: []store.Change{{Document: store.Document{Kind: "entity", ID: entity.ID, Version: 1, Data: raw}}}}
	if _, err := f.server.Exchange(ctx, "edge-a", f.registration, request); err == nil || !strings.Contains(err.Error(), "unsupported sync") {
		t.Fatal("incompatible request was not rejected", err)
	}
	for _, key := range [][2]string{{"entity", entity.ID}, {"sync_node_progress", "edge-a"}} {
		if _, err := f.cloud.Get(ctx, key[0], key[1]); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("incompatible exchange changed", key, err)
		}
	}
	request.Compatibility = nil
	if _, err := f.server.Exchange(ctx, "edge-a", f.registration, request); err != nil {
		t.Fatal("legacy edge request failed", err)
	}
	if _, err := f.cloud.Get(ctx, "entity", entity.ID); err != nil {
		t.Fatal("legacy request did not import its device", err)
	}
}

type compatibilityTransport func(*http.Request) (*http.Response, error)

func (f compatibilityTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClientRejectsIncompatibleResponseBeforeApplyingChanges(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	peer := compatibility.CurrentPeer("cloud")
	peer.Protocols["definition"] = "2.0"
	entity := model.Entity{ID: "future-cloud-asset", Kind: "asset", Version: 1}
	raw, _ := json.Marshal(entity)
	f.client.HTTP.Transport = compatibilityTransport(func(r *http.Request) (*http.Response, error) {
		var request Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			return nil, err
		}
		if request.Compatibility == nil || request.Compatibility.Protocols["sync"] != "v1" {
			t.Error("current edge did not advertise its wire formats")
		}
		body, _ := json.Marshal(Response{Compatibility: peer, UploadCursor: request.UploadCursor, Changes: []store.Change{{Document: store.Document{Kind: "entity", ID: entity.ID, Version: 1, Data: raw}}}})
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}}, nil
	})
	if err := f.client.Exchange(ctx); err == nil || !strings.Contains(err.Error(), "unsupported definition") {
		t.Fatal("incompatible response was not rejected", err)
	}
	for _, key := range [][2]string{{"entity", entity.ID}, {"sync_cursor", "cloud"}} {
		if _, err := f.edge.Get(ctx, key[0], key[1]); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("incompatible response changed", key, err)
		}
	}
}

func TestCurrentClientAcceptsLegacyCloudResponse(t *testing.T) {
	f := setup(t)
	transport := f.client.HTTP.Transport
	f.client.HTTP.Transport = compatibilityTransport(func(r *http.Request) (*http.Response, error) {
		response, err := transport.RoundTrip(r)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		var result Response
		if err = json.NewDecoder(response.Body).Decode(&result); err != nil {
			return nil, err
		}
		result.Compatibility = nil
		raw, err := json.Marshal(result)
		response.Body = io.NopCloser(bytes.NewReader(raw))
		response.ContentLength = int64(len(raw))
		return response, err
	})
	if err := f.client.Exchange(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.edge.Get(context.Background(), "entity", "factory"); err != nil {
		t.Fatal("legacy cloud changes were not applied", err)
	}
}
