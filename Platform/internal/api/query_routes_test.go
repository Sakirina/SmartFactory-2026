package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/internal/testdb"
	"competition2026/product/platform/pkg/model"
)

type queryWire struct {
	response  *http.Response
	events    chan model.QueryEvent
	heartbeat chan bool
	errors    chan error
	cancel    context.CancelFunc
}

func openQueryWire(t *testing.T, server *httptest.Server, token, kind string, opts model.QueryRequest, cursor string) *queryWire {
	t.Helper()
	raw, _ := json.Marshal(opts)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/api/sf/v1/queries/"+kind+"/events", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if cursor != "" {
		req.Header.Set("Last-Event-ID", cursor)
	}
	response, err := server.Client().Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if response.StatusCode != 200 {
		raw, _ := io.ReadAll(response.Body)
		response.Body.Close()
		cancel()
		t.Fatalf("subscribe %d: %s", response.StatusCode, raw)
	}
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatal(response.Header)
	}
	out := &queryWire{response: response, events: make(chan model.QueryEvent, 64), heartbeat: make(chan bool, 2), errors: make(chan error, 1), cancel: cancel}
	go func() {
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 4096), 32<<20)
		id := ""
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "id: ") {
				id = strings.TrimPrefix(line, "id: ")
			}
			if strings.HasPrefix(line, ": heartbeat") {
				select {
				case out.heartbeat <- true:
				default:
				}
			}
			if strings.HasPrefix(line, "data: ") {
				var event model.QueryEvent
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
					out.errors <- err
					return
				}
				if event.Cursor != "" && event.Cursor != id {
					out.errors <- fmt.Errorf("wire id differs from event cursor")
					return
				}
				select {
				case out.events <- event:
				case <-ctx.Done():
					return
				}
			}
		}
		out.errors <- scanner.Err()
	}()
	t.Cleanup(func() { out.close() })
	return out
}
func (q *queryWire) close() { q.cancel(); q.response.Body.Close() }
func (q *queryWire) event(t *testing.T, kind string) model.QueryEvent {
	t.Helper()
	deadline := time.NewTimer(6 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event := <-q.events:
			if event.Type == kind {
				return event
			}
			if event.Type == "reset" {
				t.Fatalf("unexpected reset %+v", event)
			}
		case err := <-q.errors:
			t.Fatalf("stream ended before %s: %v", kind, err)
		case <-deadline.C:
			t.Fatalf("wire timed out waiting for %s", kind)
		}
	}
}
func queryWireAwait(t *testing.T, s *Server, subscribers int) {
	t.Helper()
	deadline := time.Now().Add(7 * time.Second)
	for time.Now().Before(deadline) {
		stats := s.QueryApplication().SubscriptionStats()
		if stats.Subscribers == subscribers && (subscribers > 0 || stats.ActiveLoops == 0) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("unreleased subscriptions %+v", s.QueryApplication().SubscriptionStats())
}
func queryWireUpdate(t *testing.T, s *Server, token, name string) {
	t.Helper()
	doc, err := s.Store.Get(context.Background(), "entity", "device-a")
	if err != nil {
		t.Fatal(err)
	}
	entity, _ := store.Decode[model.Entity](doc)
	entity.Name = name
	w := call(s, token, "POST", "/api/sf/v1/entities", map[string]any{"entity": entity, "expected_version": doc.Version})
	if w.Code != 200 {
		t.Fatalf("HTTP entity write: %d %s", w.Code, w.Body.String())
	}
}
func TestQueryHTTPPaginationErrorsAndContracts(t *testing.T) {
	s, token := scopedServer(t, false)
	w := call(s, token, "POST", "/api/sf/v1/queries/entities", model.QueryRequest{Limit: 1})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var page model.QueryPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if !page.HasMore || page.NextPageToken == "" || len(page.Items) != 1 {
		t.Fatal(page)
	}
	w = call(s, token, "POST", "/api/sf/v1/queries/entities", model.QueryRequest{Limit: 1, Search: "changed", PageToken: page.NextPageToken})
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"code":"query_changed"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := s.Store.RebuildQueryProjection(context.Background()); err != nil {
		t.Fatal(err)
	}
	w = call(s, token, "POST", "/api/sf/v1/queries/entities", model.QueryRequest{Limit: 1, PageToken: page.NextPageToken})
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"clear":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call(s, token, "POST", "/api/sf/v1/queries/trend", model.QueryRequest{Limit: 2001, FromMS: 1, ToMS: 1000})
	if w.Code != 422 {
		t.Fatal(w.Code, w.Body.String())
	}
	schema, ok := publicContracts.Schema("QueryEvent")
	if !ok {
		t.Fatal("missing QueryEvent contract")
	}
	if raw, _ := json.Marshal(schema); !bytes.Contains(raw, []byte("snapshot")) {
		t.Fatal(string(raw))
	}
	operation := DefinitionOpenAPI().Paths["/api/sf/v1/queries/{kind}/events"].Post
	if operation.Responses["200"].Content["text/event-stream"] == nil {
		t.Fatal("missing typed SSE response")
	}
}
func TestQuerySSEHTTPIdleAndRecovery(t *testing.T) {
	for _, protocol := range []string{"HTTP/1.1", "HTTP/2.0"} {
		t.Run(protocol, func(t *testing.T) {
			s, token := scopedServer(t, false)
			s.QueryApplication().PollInterval = 10 * time.Millisecond
			server := httptest.NewUnstartedServer(s.Handler())
			if protocol == "HTTP/2.0" {
				server.EnableHTTP2 = true
				server.StartTLS()
			} else {
				server.Start()
			}
			defer server.Close()
			opts := model.QueryRequest{Limit: 10, EntityKind: "device"}
			wire := openQueryWire(t, server, token, "entities", opts, "")
			if wire.response.Proto != protocol {
				t.Fatalf("negotiated %s", wire.response.Proto)
			}
			snapshot := wire.event(t, "snapshot")
			if len(snapshot.Page.Items) != 1 {
				t.Fatal(snapshot)
			}
			companion := openQueryWire(t, server, token, "entities", opts, "")
			if other := companion.event(t, "snapshot"); other.Cursor != snapshot.Cursor {
				t.Fatal("shared snapshots differ")
			}
			beforeReads := s.QueryApplication().SubscriptionStats().PageReads
			started := time.Now()
			select {
			case <-wire.heartbeat:
			case err := <-wire.errors:
				t.Fatalf("idle connection failed: %v", err)
			case <-time.After(18 * time.Second):
				t.Fatal("heartbeat did not arrive")
			}
			idle := time.Since(started)
			if idle < 14*time.Second {
				t.Fatalf("heartbeat too early %s", idle)
			}
			queryWireUpdate(t, s, token, "after heartbeat")
			changed := wire.event(t, "delta")
			if len(changed.Changes) != 1 || !bytes.Contains(changed.Changes[0].Item.Data, []byte("after heartbeat")) {
				t.Fatal(changed)
			}
			if other := companion.event(t, "delta"); other.Cursor != changed.Cursor {
				t.Fatal("shared deltas differ")
			}
			if s.QueryApplication().SubscriptionStats().PageReads-beforeReads != 1 {
				t.Fatal("each HTTP subscriber computed its own window")
			}
			wire.close()
			companion.close()
			queryWireAwait(t, s, 0)
			queryWireUpdate(t, s, token, "while disconnected")
			resumed := openQueryWire(t, server, token, "entities", opts, changed.Cursor)
			delta := resumed.event(t, "delta")
			if len(delta.Changes) != 1 || !bytes.Contains(delta.Changes[0].Item.Data, []byte("while disconnected")) {
				t.Fatal(delta)
			}
			userDoc, err := s.Store.Get(context.Background(), "user", "user")
			if err != nil {
				t.Fatal(err)
			}
			user, _ := store.Decode[model.User](userDoc)
			user.Active = false
			user.Version = userDoc.Version + 1
			if _, err = s.Store.Put(context.Background(), "user", "user", userDoc.Version, user); err != nil {
				t.Fatal(err)
			}
			reset := resumed.event(t, "reset")
			if !reset.Clear || reset.Retryable {
				t.Fatal(reset)
			}
			resumed.close()
			queryWireAwait(t, s, 0)
			t.Logf("%s idle %s crossed heartbeat; same connection delivered HTTP mutation; last-event-id recovered missed update; revoked account reset cleared cache", protocol, idle)
		})
	}
}
func TestQuerySSESlowSocketDoesNotBlockOtherClients(t *testing.T) {
	s, token := scopedServer(t, false)
	s.QueryApplication().PollInterval = 10 * time.Millisecond
	payload := strings.Repeat("opaque-data-", 12000)
	if err := s.Store.Write(context.Background(), func(tx *store.Tx) error {
		for i := 0; i < 64; i++ {
			e := model.Entity{ID: fmt.Sprintf("bulk-%02d", i), Name: "Bulk", Kind: "device", ParentID: "a", Tags: map[string]string{"payload": payload}}
			if _, err := tx.Put("entity", e.ID, 0, e); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	fast := openQueryWire(t, server, token, "entities", model.QueryRequest{Limit: 10, ResourceIDs: []string{"device-a"}}, "")
	fast.event(t, "snapshot")
	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if tcp, ok := conn.(*net.TCPConn); ok {
		if err = tcp.SetReadBuffer(1024); err != nil {
			t.Fatal(err)
		}
	}
	body := `{"limit":100,"entity_kind":"device"}`
	req, _ := http.NewRequest("POST", server.URL+"/api/sf/v1/queries/entities/events", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if err = req.Write(conn); err != nil {
		t.Fatal(err)
	}
	queryWireAwait(t, s, 2)
	started := time.Now()
	queryWireUpdate(t, s, token, "other client remains active")
	delta := fast.event(t, "delta")
	if len(delta.Changes) != 1 {
		t.Fatal(delta)
	}
	queryWireAwait(t, s, 1)
	elapsed := time.Since(started)
	if elapsed > 6*time.Second {
		t.Fatalf("slow socket deadline %s", elapsed)
	}
	fast.close()
	conn.Close()
	queryWireAwait(t, s, 0)
	t.Logf("8.4 MB initial snapshot exceeded unread TCP window; slow connection released within %s while another client received its delta", elapsed)
}
func TestQuerySSEPostgresSnapshotDeltaResumeReset(t *testing.T) {
	dsn, _ := testdb.Postgres(t, "query_wire")
	db, err := store.Open(context.Background(), dsn, "query-wire", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	db.DB.SetMaxOpenConns(3)
	db.DB.SetMaxIdleConns(1)
	defer db.Close()
	auth := &identity.Manager{Store: db, Master: make([]byte, 32)}
	if _, err = auth.CreateUser(context.Background(), model.Actor{}, model.User{ID: "user", Name: "Reader", Login: "user", Active: true, Roles: []string{"engineer"}, Resources: []string{"*"}}, "testing-password-1234", "", 0); err != nil {
		t.Fatal(err)
	}
	token, _, err := auth.Login(context.Background(), "user", "testing-password-1234", "", false, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Put(context.Background(), "entity", "device-a", 0, model.Entity{ID: "device-a", Kind: "device", Name: "Original"}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Put(context.Background(), "entity", "late", 0, model.Entity{ID: "late", Kind: "device", Name: "Before"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: db, Identity: auth, Mode: "cloud"}
	s.QueryApplication().PollInterval = 10 * time.Millisecond
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	opts := model.QueryRequest{Limit: 10}
	wire := openQueryWire(t, server, token, "entities", opts, "")
	snapshot := wire.event(t, "snapshot")
	lateCtx, lateCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer lateCancel()
	prepared, release, completed := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		completed <- db.Write(lateCtx, func(tx *store.Tx) error {
			if _, err := tx.Put("entity", "late", 1, model.Entity{ID: "late", Kind: "device", Name: "Late committed"}); err != nil {
				return err
			}
			close(prepared)
			select {
			case <-release:
				return nil
			case <-lateCtx.Done():
				return lateCtx.Err()
			}
		})
	}()
	select {
	case <-prepared:
	case e := <-completed:
		t.Fatal(e)
	case <-lateCtx.Done():
		t.Fatal(lateCtx.Err())
	}
	queryWireUpdate(t, s, token, "Early committed")
	early := wire.event(t, "delta")
	if len(early.Changes) != 1 || early.Changes[0].ID != "device-a" {
		t.Fatal("early stream included uncommitted row", early)
	}
	close(release)
	if err = <-completed; err != nil {
		t.Fatal(err)
	}
	late := wire.event(t, "delta")
	if len(late.Changes) != 1 || late.Changes[0].ID != "late" || late.Cursor == early.Cursor {
		t.Fatal("late commit missing from stream", late)
	}
	snapshot.Cursor = late.Cursor
	wire.close()
	queryWireAwait(t, s, 0)
	queryWireUpdate(t, s, token, "Postgres changed")
	resumed := openQueryWire(t, server, token, "entities", opts, snapshot.Cursor)
	delta := resumed.event(t, "delta")
	if len(delta.Changes) != 1 {
		t.Fatal(delta)
	}
	if err = db.RebuildQueryProjection(context.Background()); err != nil {
		t.Fatal(err)
	}
	reset := resumed.event(t, "reset")
	if reset.Reason != "projection_rebuilt" || !reset.Clear {
		t.Fatal(reset)
	}
	resumed.close()
	queryWireAwait(t, s, 0)
}

func TestQuerySSEExpiredCursor(t *testing.T) {
	s, token := scopedServer(t, false)
	s.QueryApplication().PollInterval = 10 * time.Millisecond
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	opts := model.QueryRequest{Limit: 10}
	wire := openQueryWire(t, server, token, "entities", opts, "")
	initial := wire.event(t, "snapshot")
	wire.close()
	queryWireAwait(t, s, 0)
	for i := 0; i < 3; i++ {
		queryWireUpdate(t, s, token, fmt.Sprintf("later-%d", i))
	}
	if err := s.Store.CompactQueryHistory(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	expired := openQueryWire(t, server, token, "entities", opts, initial.Cursor)
	event := expired.event(t, "reset")
	if event.Reason != "cursor_expired" || !event.Clear {
		t.Fatal(event)
	}
	expired.close()
	queryWireAwait(t, s, 0)
}
