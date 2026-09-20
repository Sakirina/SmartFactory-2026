package cloudsync

import (
	"context"
	"encoding/json"
	"testing"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func TestProposalAndDecisionRoundTripUnderNodeOwnership(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := model.AssetProposal{ID: "edge-a:proposal", NodeID: "edge-a", Status: "pending", Version: 1, Entity: model.Entity{ID: "asset-proposed", Kind: "asset", Name: "Proposed", Status: "active"}}
	if _, err := f.edge.Put(ctx, "asset_proposal", p.ID, 0, p); err != nil {
		t.Fatal(err)
	}
	if err := f.client.Exchange(ctx); err != nil {
		t.Fatal(err)
	}
	doc, err := f.cloud.Get(ctx, "asset_proposal", p.ID)
	if err != nil {
		t.Fatal(err)
	}
	p.Version = 2
	p.Status = "approved"
	if _, err = f.cloud.Put(ctx, "asset_proposal", p.ID, doc.Version, p); err != nil {
		t.Fatal(err)
	}
	if err = f.client.Exchange(ctx); err != nil {
		t.Fatal(err)
	}
	doc, err = f.edge.Get(ctx, "asset_proposal", p.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Decode[model.AssetProposal](doc)
	if err != nil || got.Status != "approved" {
		t.Fatal(got, err)
	}
	if err = f.client.Exchange(ctx); err != nil {
		t.Fatal("decision was incorrectly uploaded as a new proposal", err)
	}
	p.ID = "edge-a:spoof"
	p.NodeID = "edge-b"
	p.Status = "pending"
	p.Version = 1
	raw, _ := json.Marshal(p)
	rawDoc := store.Document{Kind: "asset_proposal", ID: p.ID, Version: 1, Data: raw}
	if err = f.server.importChanges(ctx, "edge-a", []store.Change{{Document: rawDoc}}, true); err == nil {
		t.Fatal("node spoof accepted")
	}
}
