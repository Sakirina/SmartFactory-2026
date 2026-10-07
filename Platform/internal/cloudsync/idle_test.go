package cloudsync

import (
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestIdleExchangesDoNotWriteUntilHeartbeat(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	now := time.Now()
	f.cloud.Now = func() time.Time { return now }
	f.edge.Now = func() time.Time { return now }
	for range 4 {
		if err := f.client.Exchange(ctx); err != nil {
			t.Fatal(err)
		}
	}
	changes := func(s *store.Store) int64 {
		var n int64
		if err := s.DB.QueryRow("SELECT total_changes()").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	cloudBefore, edgeBefore := changes(f.cloud), changes(f.edge)
	// Compare the full domain state as well as SQLite's aggregate counter:
	// a heartbeat also maintains the committed query projection.
	tables := func(s *store.Store) map[string]string {
		rows, err := s.DB.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for rows.Next() {
			var name string
			if err = rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			names = append(names, name)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		out := map[string]string{}
		for _, name := range names {
			where := ""
			if name == "documents" {
				where = " WHERE NOT(kind='source' AND id='edge-a')"
			}
			rows, err = s.DB.Query("SELECT * FROM \"" + strings.ReplaceAll(name, "\"", "\"\"") + "\"" + where)
			if err != nil {
				t.Fatal(err)
			}
			columns, err := rows.Columns()
			if err != nil {
				t.Fatal(err)
			}
			var hashes []string
			for rows.Next() {
				values := make([]any, len(columns))
				pointers := make([]any, len(columns))
				for i := range values {
					pointers[i] = &values[i]
				}
				if err = rows.Scan(pointers...); err != nil {
					t.Fatal(err)
				}
				hashes = append(hashes, store.Hash(values))
			}
			if err = rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
			sort.Strings(hashes)
			out[name] = store.Hash(hashes)
		}
		return out
	}
	beforeTables := tables(f.cloud)
	beforeState, err := f.cloud.QueryState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeSource, err := f.cloud.Get(ctx, "source", "edge-a")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(4 * time.Second)
	for range 25 {
		if err := f.client.Exchange(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := changes(f.cloud) - cloudBefore; n != 0 {
		t.Fatalf("idle cloud changed %d rows", n)
	}
	if n := changes(f.edge) - edgeBefore; n != 0 {
		t.Fatalf("idle edge changed %d rows", n)
	}
	now = now.Add(time.Second)
	if err := f.client.Exchange(ctx); err != nil {
		t.Fatal(err)
	}
	afterSource, err := f.cloud.Get(ctx, "source", "edge-a")
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.Decode[model.SourceState](afterSource)
	if err != nil || source.ID != "edge-a" || source.LastSeenMS != now.UnixMilli() || source.Status != "online" || afterSource.Version != beforeSource.Version+1 {
		t.Fatalf("heartbeat source mismatch: %+v version %d -> %d error %v", source, beforeSource.Version, afterSource.Version, err)
	}
	afterState, err := f.cloud.QueryState(ctx)
	if err != nil || afterState.Head != beforeState.Head+1 || afterState.Epoch != beforeState.Epoch || afterState.AuthRevision != beforeState.AuthRevision {
		t.Fatalf("heartbeat query state: %+v -> %+v, %v", beforeState, afterState, err)
	}
	commits, err := f.cloud.QueryCommits(ctx, beforeState.Head, 10)
	if err != nil || len(commits) != 1 || len(commits[0].Kinds) != 1 || commits[0].Kinds[0] != "sources" || commits[0].Sequence != afterState.Head {
		t.Fatalf("heartbeat query commits: %+v, %v", commits, err)
	}
	page, err := f.cloud.QueryPage(ctx, "sources", model.QueryRequest{Limit: 10}, store.QueryScope{All: true, AuthRevision: -1}, store.QueryPosition{})
	if err != nil || len(page.Rows) != 1 {
		t.Fatalf("heartbeat projection: %+v, %v", page, err)
	}
	var projected model.SourceState
	if err = store.DecodeJSON(page.Rows[0].Data, &projected); err != nil || store.Hash(projected) != store.Hash(source) || page.Rows[0].Revision != fmt.Sprint(afterState.Head) {
		t.Fatalf("heartbeat projection differs from source: %+v, %v", page.Rows[0], err)
	}
	afterTables := tables(f.cloud)
	var changedTables []string
	for name, hash := range beforeTables {
		if afterTables[name] == hash {
			continue
		}
		changedTables = append(changedTables, name)
		if name != "sf_query_rows" && name != "sf_query_resources" && name != "sf_query_commits" && name != "sf_query_state" {
			t.Fatalf("heartbeat changed unrelated table %s", name)
		}
	}
	sort.Strings(changedTables)
	t.Logf("heartbeat node=%s committed source revision=%s, aggregate writes=%d, associated tables=%v; other domain rows unchanged", source.ID, page.Rows[0].Revision, changes(f.cloud)-cloudBefore, changedTables)
	if n := changes(f.edge) - edgeBefore; n != 0 {
		t.Fatalf("heartbeat wrote unchanged edge cursor: %d", n)
	}
	now = now.Add(16 * time.Second)
	sources, err := f.cloud.Sources(ctx)
	if err != nil || len(sources) != 1 || sources[0].Status != "offline" {
		t.Fatal(sources, err)
	}
}
