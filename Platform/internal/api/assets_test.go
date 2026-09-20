package api

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func TestAssetProposalNeedsCloudReviewAndPreservesCurrentVersion(t *testing.T) {
	s, token := scopedServer(t, false)
	s.Mode = "edge"
	s.NodeID = "edge-a"
	entity := model.Entity{ID: "area-new", Kind: "asset", Name: "New area", ParentID: "a", Status: "active"}
	w := call(s, token, "POST", "/api/sf/v1/entities", map[string]any{"entity": entity, "expected_version": 0})
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var proposal model.AssetProposal
	if err := json.Unmarshal(w.Body.Bytes(), &proposal); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Get(context.Background(), "entity", entity.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("pending proposal changed live asset", err)
	}
	body := map[string]any{"approve": true, "reason": "reviewed", "expected_version": 1}
	if w = call(s, token, "POST", "/api/sf/v1/asset-proposals/"+proposal.ID+"/decide", body); w.Code == 200 {
		t.Fatal("edge approved own asset proposal")
	}
	s.Mode = "cloud"
	if w = call(s, token, "POST", "/api/sf/v1/asset-proposals/"+proposal.ID+"/decide", body); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	doc, err := s.Store.Get(context.Background(), "entity", entity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Version != 1 {
		t.Fatal(doc.Version)
	}
	if w = call(s, token, "POST", "/api/sf/v1/asset-proposals/"+proposal.ID+"/decide", body); w.Code != 409 {
		t.Fatal("duplicate review changed version", w.Code)
	}
	s.Mode = "edge"
	entity.ParentID = "b"
	if w = call(s, token, "POST", "/api/sf/v1/entities", map[string]any{"entity": entity, "expected_version": 1}); w.Code != 403 {
		t.Fatal("foreign asset move allowed", w.Code)
	}
}
