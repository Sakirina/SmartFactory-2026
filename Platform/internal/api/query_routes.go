package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"time"

	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/pkg/model"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

type QueryRequest struct {
	Kind string `path:"kind" enum:"entities,alarms,executions,trend"`
	Body model.QueryRequest
}
type QueryResponse struct{ Body model.QueryPage }
type QueryEventsRequest struct {
	Kind        string `path:"kind" enum:"entities,alarms,executions,trend"`
	LastEventID string `header:"Last-Event-ID" maxLength:"4096"`
	Body        model.QueryRequest
}

func QueryContractTypes() []any {
	return []any{model.QueryRequest{}, model.QueryRow{}, model.QueryPage{}, model.QueryChange{}, model.QueryEvent{}, model.QueryMetadata{}}
}
func (s *Server) QueryApplication() *application.Queries {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.queries == nil {
		s.queries = &application.Queries{Store: s.Store, Identity: s.Identity, Control: s.Control}
	}
	return s.queries
}

func (s *Server) registerQueryRoutes(api huma.API, operation func(string, string, string) huma.Operation) {
	page := operation("post_queries_kind", "/queries/{kind}", "read")
	huma.Register(api, page, func(ctx context.Context, in *QueryRequest) (*QueryResponse, error) {
		p, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		out, err := s.QueryApplication().Page(ctx, p, in.Kind, in.Body)
		return &QueryResponse{Body: out}, contractError(err)
	})
	events := operation("post_queries_kind_events", "/queries/{kind}/events", "read")
	events.Description = "SSE snapshot, delta, checkpoint and reset events. The signed string event ID is the resumable cursor. The subscription maintains the first limit rows under the current identity and authorization."
	events.Responses["200"] = &huma.Response{Description: "UTF-8 Server Sent Events; id is an opaque signed string", Content: map[string]*huma.MediaType{"text/event-stream": {Schema: api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[model.QueryEvent](), true, "QueryEvent")}}}
	huma.Register(api, events, func(ctx context.Context, in *QueryEventsRequest) (*huma.StreamResponse, error) {
		p, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		sub, err := s.QueryApplication().Subscribe(ctx, p, in.Kind, in.Body, in.LastEventID)
		if err != nil {
			return nil, contractError(err)
		}
		return &huma.StreamResponse{Body: func(hctx huma.Context) { r, w := humago.Unwrap(hctx); s.streamQueries(w, r, sub) }}, nil
	})
}

func (s *Server) streamQueries(w http.ResponseWriter, r *http.Request, sub *application.QuerySubscription) {
	defer sub.Close()
	controller := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	send := func(event *model.QueryEvent) error {
		if err := controller.SetWriteDeadline(time.Now().Add(3 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		defer controller.SetWriteDeadline(time.Time{})
		if event == nil {
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return err
			}
		} else {
			raw, err := json.Marshal(event)
			if err != nil {
				return err
			}
			if event.Cursor != "" {
				if _, err = fmt.Fprintf(w, "id: %s\n", event.Cursor); err != nil {
					return err
				}
			}
			if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, raw); err != nil {
				return err
			}
		}
		return controller.Flush()
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-sub.Events:
			if !ok {
				return
			}
			if err := send(&event); err != nil {
				return
			}
			if event.Type == "reset" {
				return
			}
		case <-heartbeat.C:
			if err := send(nil); err != nil {
				return
			}
		}
	}
}

// The legacy URL uses the same shared subscription mechanism. New clients use
// the typed POST operation to retain explicit filters and snapshot cursors.
func (s *Server) queryEventsLegacy(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	window := number(r.URL.Query().Get("window_ms"), 3600000)
	now := s.Store.Now().UnixMilli()
	opts := model.QueryRequest{FromMS: number(r.URL.Query().Get("from_ms"), now-window), ToMS: number(r.URL.Query().Get("to_ms"), now+window), Limit: int(number(r.URL.Query().Get("limit"), 2000)), Resolution: r.URL.Query().Get("resolution")}
	if value := r.URL.Query().Get("device_ids"); value != "" {
		opts.ResourceIDs = strings.Split(value, ",")
	}
	if value := r.URL.Query().Get("keys"); value != "" {
		opts.Keys = strings.Split(value, ",")
	}
	sub, err := s.QueryApplication().Subscribe(r.Context(), p, "trend", opts, r.Header.Get("Last-Event-ID"))
	if err != nil {
		return err
	}
	s.streamQueries(w, r, sub)
	return nil
}
