package cloudsync

import (
	"context"
	"testing"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func TestPublicationWaitsForCommittedNodeCursor(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	d := model.Definition{ID: "published", Name: "Published", Kind: "analysis", SchemaVersion: model.ContractVersion, GroupID: "factory", Status: "published", Version: 1, Nodes: []model.Node{{ID: "input", Type: "input"}}}
	if err := f.cloud.Write(ctx, func(tx *store.Tx) error {
		if _, e := tx.Put("definition", d.ID, 0, d); e != nil {
			return e
		}
		return tx.Enqueue("publication", "edge_definition", d.ID, d)
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.server.CompleteMetadata(ctx); err != nil {
		t.Fatal(err)
	}
	items, _ := f.cloud.Deliveries(ctx, "edge_definition", 100)
	if len(items) != 1 {
		t.Fatal("publication completed without node acknowledgement")
	}
	if err := f.client.Exchange(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.server.CompleteMetadata(ctx); err != nil {
		t.Fatal(err)
	}
	items, _ = f.cloud.Deliveries(ctx, "edge_definition", 100)
	if len(items) != 1 {
		t.Fatal("response treated as a node acknowledgement")
	}
	if err := f.client.Exchange(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.server.CompleteMetadata(ctx); err != nil {
		t.Fatal(err)
	}
	items, _ = f.cloud.Deliveries(ctx, "edge_definition", 100)
	if len(items) != 0 {
		t.Fatal("committed node cursor did not complete publication")
	}
}
