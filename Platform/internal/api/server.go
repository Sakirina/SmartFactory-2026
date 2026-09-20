package api

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"competition2026/product/platform/internal/configcenter"
	"competition2026/product/platform/internal/control"
	"competition2026/product/platform/internal/engine"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/plugins"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type Server struct {
	Store         *store.Store
	Identity      *identity.Manager
	Engine        *engine.Service
	Control       *control.Service
	Config        *configcenter.Service
	Mode          string
	NodeID        string
	ServiceToken  string
	StaticDir     string
	ConfigURL     string
	HTTPClient    *http.Client
	MCP           http.Handler
	Chat          http.Handler
	mu            sync.Mutex
	loginAttempts map[string]attempt
}
type attempt struct {
	Count int
	At    time.Time
}
type handler func(http.ResponseWriter, *http.Request, identity.Principal) error
type APIError struct {
	Status  int
	Message string
}

func (e APIError) Error() string { return e.Message }
func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	if s.Mode == "config" && s.Config != nil {
		s.Config.RegisterInternal(m, s.ServiceToken)
	}
	m.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if e := s.Store.DB.PingContext(r.Context()); e != nil {
			respond(w, 503, map[string]any{"status": "unavailable"})
			return
		}
		respond(w, 200, map[string]any{"status": "ok", "node_id": s.NodeID, "mode": s.Mode, "contract_version": model.ContractVersion})
	})
	m.HandleFunc("POST /api/sf/v1/login", s.login)
	m.HandleFunc("POST /internal/native/evaluate", s.nativeEvaluate)
	s.route(m, "POST /api/sf/v1/logout", "read", func(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
		e := s.Identity.Logout(r.Context(), bearer(r))
		if e == nil {
			respond(w, 200, map[string]bool{"ok": true})
		}
		return e
	})
	s.route(m, "GET /api/sf/v1/me", "read", func(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
		respond(w, 200, map[string]any{"user": p.User, "local": p.Local, "step_up_until_ms": p.StepUpUntilMS, "mode": s.Mode, "node_id": s.NodeID, "refresh_ms": s.Store.Policy().RefreshMS})
		return nil
	})
	s.route(m, "GET /api/sf/v1/overview", "read", s.overview)
	s.route(m, "GET /api/sf/v1/contracts", "read", s.contracts)
	s.route(m, "GET /api/sf/v1/contracts/{name}", "read", s.contracts)
	s.route(m, "GET /api/sf/v1/dashboards", "read", s.dashboards)
	s.route(m, "POST /api/sf/v1/dashboards", "dashboard", s.putDashboard)
	s.route(m, "GET /api/sf/v1/entities", "read", s.entities)
	s.route(m, "POST /api/sf/v1/entities", "register", s.putEntity)
	s.route(m, "GET /api/sf/v1/asset-proposals", "read", s.assetProposals)
	s.route(m, "POST /api/sf/v1/asset-proposals/{id}/decide", "register", s.decideAssetProposal)
	s.route(m, "GET /api/sf/v1/data", "read", s.data)
	s.route(m, "POST /api/sf/v1/ingest", "ingest", s.ingest)
	s.route(m, "GET /api/sf/v1/events", "read", s.events)
	s.route(m, "GET /api/sf/v1/catalogue", "read", s.catalogue)
	s.route(m, "GET /api/sf/v1/definitions", "read", s.definitions)
	s.route(m, "GET /api/sf/v1/definitions/{id}/versions", "read", s.definitionVersions)
	s.route(m, "GET /api/sf/v1/drafts", "draft", s.drafts)
	s.route(m, "POST /api/sf/v1/drafts", "draft", s.saveDraft)
	s.route(m, "POST /api/sf/v1/drafts/{id}/validate", "draft", s.validateDraft)
	s.route(m, "GET /api/sf/v1/drafts/{id}/diff", "read", s.draftDiff)
	s.route(m, "POST /api/sf/v1/drafts/{id}/simulate", "draft", s.simulateDraft)
	s.route(m, "POST /api/sf/v1/drafts/{id}/publish", "publish", s.publishDraft)
	s.route(m, "POST /api/sf/v1/definitions/{id}/deactivate", "publish", s.deactivate)
	s.route(m, "POST /api/sf/v1/definitions/{id}/rollback", "draft", s.rollback)
	s.route(m, "GET /api/sf/v1/executions", "read", s.executions)
	s.route(m, "POST /api/sf/v1/executions", "control", s.createExecution)
	s.route(m, "POST /api/sf/v1/executions/{id}/approve", "approve", s.approve)
	s.route(m, "POST /api/sf/v1/executions/{id}/dispatch", "control", s.dispatch)
	s.route(m, "GET /api/sf/v1/alarms", "read", s.alarms)
	s.route(m, "POST /api/sf/v1/alarms/{id}/acknowledge", "approve", s.ackAlarm)
	s.route(m, "GET /api/sf/v1/jobs", "read", s.jobs)
	s.route(m, "POST /api/sf/v1/jobs", "register", s.createJob)
	s.route(m, "POST /api/sf/v1/jobs/{id}/retry", "register", s.retryJob)
	s.route(m, "GET /api/sf/v1/notifications", "read", s.notifications)
	s.route(m, "GET /api/sf/v1/organization", "identity", s.organization)
	s.route(m, "POST /api/sf/v1/organization/sync", "identity", s.syncOrganization)
	s.route(m, "POST /api/sf/v1/users", "identity", s.putUser)
	s.route(m, "POST /api/sf/v1/grants", "identity", s.putGrant)
	s.route(m, "GET /api/sf/v1/plugins", "config", s.listPlugins)
	s.route(m, "POST /api/sf/v1/plugins", "config", s.putPlugin)
	if s.Mode == "cloud" {
		manager := &plugins.Manager{Store: s.Store, Config: s.Config}
		m.HandleFunc("POST /internal/plugins/{id}/organization", manager.Push)
	}
	s.route(m, "GET /api/sf/v1/config", "config", s.listConfig)
	s.route(m, "POST /api/sf/v1/config", "config", s.putConfig)
	s.route(m, "POST /api/sf/v1/config/{id}/acknowledge", "config", s.ackConfig)
	s.route(m, "GET /api/sf/v1/audit", "audit", s.audit)
	s.route(m, "POST /api/sf/v1/audit/verify", "audit", s.verifyAudit)
	s.route(m, "GET /api/sf/v1/runtime", "read", s.runtime)
	if s.MCP != nil {
		m.Handle("/mcp/", s.MCP)
	}
	if s.Chat != nil {
		m.Handle("/api/sf/v1/assistant", s.Chat)
	}
	if s.StaticDir != "" {
		fs := http.FileServer(http.Dir(s.StaticDir))
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				respond(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
				return
			}
			switch r.URL.Path {
			case "/edge":
				http.ServeFile(w, r, filepath.Join(s.StaticDir, "edge.html"))
			case "/screen":
				http.ServeFile(w, r, filepath.Join(s.StaticDir, "screen.html"))
			default:
				fs.ServeHTTP(w, r)
			}
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("Origin") != "" {
			origin := r.Header.Get("Origin")
			if origin != "http://"+r.Host && origin != "https://"+r.Host {
				respond(w, 403, map[string]string{"error": "origin not allowed"})
				return
			}
		}
		m.ServeHTTP(w, r)
	})
}
func (s *Server) route(m *http.ServeMux, pattern, action string, h handler) {
	m.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		p, e := s.Authenticate(r)
		if e == nil {
			e = s.Identity.Permit(r.Context(), p, action, "")
		}
		if e == nil {
			e = h(w, r, p)
		}
		if e != nil {
			fail(w, e)
		}
	})
}
func bearer(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}
func (s *Server) Authenticate(r *http.Request) (identity.Principal, error) {
	token := bearer(r)
	if s.ServiceToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.ServiceToken)) == 1 {
		p := identity.Principal{User: model.User{ID: "service:" + s.NodeID, Name: "Platform service", Active: true, Roles: []string{"gateway"}, Resources: []string{"*"}}, Actor: model.Actor{UserID: "service:" + s.NodeID, Source: "service"}}
		if s.Mode == "config" {
			p.User.Roles = []string{"admin"}
			if encoded := r.Header.Get("X-SF-Actor"); encoded != "" {
				b, e := base64.StdEncoding.DecodeString(encoded)
				if e != nil {
					return p, identity.ErrAuthentication
				}
				if e = json.Unmarshal(b, &p.Actor); e != nil {
					return p, e
				}
				p.User.AI = p.Actor.AI
			}
		}
		return p, nil
	}
	return s.Identity.Authenticate(r.Context(), token)
}
func decode(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 2<<20)
	d := json.NewDecoder(r.Body)
	d.UseNumber()
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return APIError{400, "invalid JSON: " + e.Error()}
	}
	var extra any
	if e := d.Decode(&extra); !errors.Is(e, io.EOF) {
		return APIError{400, "one JSON value is required"}
	}
	return nil
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, e error) {
	code := 400
	var apiErr APIError
	switch {
	case errors.As(e, &apiErr):
		code = apiErr.Status
	case errors.Is(e, identity.ErrAuthentication):
		code = 401
	case errors.Is(e, identity.ErrDenied):
		code = 403
	case errors.Is(e, store.ErrNotFound):
		code = 404
	case errors.Is(e, store.ErrConflict):
		code = 409
	case errors.Is(e, context.DeadlineExceeded):
		code = 504
	}
	respond(w, code, map[string]string{"error": e.Error()})
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Login    string `json:"login"`
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if e := decode(r, &req); e != nil {
		fail(w, e)
		return
	}
	s.mu.Lock()
	if s.loginAttempts == nil {
		s.loginAttempts = map[string]attempt{}
	}
	key := r.RemoteAddr
	if idx := strings.LastIndex(key, ":"); idx >= 0 {
		key = key[:idx]
	}
	a := s.loginAttempts[key]
	if time.Since(a.At) > time.Minute {
		a = attempt{At: time.Now()}
	}
	a.Count++
	s.loginAttempts[key] = a
	for ip, v := range s.loginAttempts {
		if time.Since(v.At) > 2*time.Minute {
			delete(s.loginAttempts, ip)
		}
	}
	s.mu.Unlock()
	if a.Count > 20 {
		fail(w, APIError{429, "too many login attempts"})
		return
	}
	token, p, e := s.Identity.Login(r.Context(), req.Login, req.Password, req.Code, s.Mode == "edge", r.RemoteAddr)
	if e != nil {
		fail(w, e)
		return
	}
	respond(w, 200, map[string]any{"token": token, "user": p.User, "local": p.Local, "step_up_until_ms": p.StepUpUntilMS})
}
func (s *Server) allow(r *http.Request, p identity.Principal, action, resource string) error {
	return s.Identity.Permit(r.Context(), p, action, resource)
}
func (s *Server) overview(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	entities, e := s.visibleEntities(r, p)
	if e != nil {
		return e
	}
	sources, e := s.Store.Sources(r.Context())
	if e != nil {
		return e
	}
	alarms, e := s.visibleDocuments(r, p, "alarm")
	if e != nil {
		return e
	}
	active := 0
	for _, doc := range alarms {
		v, e := store.Decode[model.Alarm](doc)
		if e != nil {
			return e
		}
		if v.Active {
			active++
		}
	}
	var pending int64
	if e = s.Store.DB.QueryRowContext(r.Context(), "SELECT count(*) FROM outbox").Scan(&pending); e != nil {
		return e
	}
	respond(w, 200, map[string]any{"node_id": s.NodeID, "mode": s.Mode, "entities": len(entities), "active_alarms": active, "sources": s.filterSources(r, p, sources), "pending_deliveries": pending, "server_time_ms": s.Store.Now().UnixMilli()})
	return nil
}
func (s *Server) visibleEntities(r *http.Request, p identity.Principal) ([]model.Entity, error) {
	docs, e := s.Store.List(r.Context(), "entity")
	if e != nil {
		return nil, e
	}
	out := []model.Entity{}
	for _, d := range docs {
		entity, e := store.Decode[model.Entity](d)
		if e != nil {
			return nil, e
		}
		entity.Version = d.Version
		if s.allow(r, p, "read", entity.ID) == nil {
			if mapping, err := s.Store.Get(r.Context(), "tb_mapping", entity.ID); err == nil {
				var value struct {
					Native struct {
						ID string `json:"id"`
					} `json:"native"`
				}
				if err = store.DecodeJSON(mapping.Data, &value); err != nil {
					return nil, err
				}
				entity.TBID = value.Native.ID
			} else if !errors.Is(err, store.ErrNotFound) {
				return nil, err
			}
			out = append(out, entity)
		}
	}
	return out, nil
}
func (s *Server) entities(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	out, e := s.visibleEntities(r, p)
	if e == nil {
		respond(w, 200, out)
	}
	return e
}
func (s *Server) putEntity(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	var req struct {
		Entity          model.Entity `json:"entity"`
		ExpectedVersion int64        `json:"expected_version"`
	}
	if e := decode(r, &req); e != nil {
		return e
	}
	entity := req.Entity
	if entity.ID == "" {
		entity.ID = identity.ID()
	}
	if entity.Name == "" || !has([]string{"asset", "device", "edge"}, entity.Kind) {
		return errors.New("entity requires a name and kind")
	}
	resource := entity.ID
	if req.ExpectedVersion == 0 {
		resource = entity.ParentID
		if resource == "" {
			resource = "*"
		}
	}
	if e := s.allow(r, p, "register", resource); e != nil {
		return e
	}
	if req.ExpectedVersion < 0 {
		return APIError{400, "nonnegative version is required"}
	}
	if entity.ParentID != "" {
		if e := s.allow(r, p, "register", entity.ParentID); e != nil {
			return e
		}
	}
	if old, e := s.Store.Get(r.Context(), "entity", entity.ID); e == nil {
		previous, e := store.Decode[model.Entity](old)
		if e != nil {
			return e
		}
		if previous.Kind != entity.Kind {
			return APIError{400, "entity kind cannot change"}
		}
		if s.Mode == "edge" && previous.Kind == "device" && previous.EdgeID != s.NodeID {
			return identity.ErrDenied
		}
	} else if !errors.Is(e, store.ErrNotFound) {
		return e
	}
	if entity.Kind == "device" && entity.Status == "approved" && s.Mode != "edge" {
		return errors.New("physical devices must be approved through their edge")
	}
	if entity.Kind == "device" && s.Mode == "edge" {
		entity.EdgeID = s.NodeID
	}
	for id, n := entity.ParentID, 0; id != ""; n++ {
		if id == entity.ID || n > 64 {
			return errors.New("entity hierarchy contains a cycle")
		}
		d, e := s.Store.Get(r.Context(), "entity", id)
		if e != nil {
			return e
		}
		parent, e := store.Decode[model.Entity](d)
		if e != nil {
			return e
		}
		id = parent.ParentID
	}
	entity.Version = req.ExpectedVersion + 1
	if entity.Kind == "asset" && s.Mode == "edge" {
		return s.proposeAsset(w, r, p, entity, req.ExpectedVersion)
	}
	e := s.Store.Write(r.Context(), func(t *store.Tx) error {
		if _, e := t.Put("entity", entity.ID, req.ExpectedVersion, entity); e != nil {
			return e
		}
		if e := t.Enqueue(fmt.Sprintf("entity:%s:%d", entity.ID, entity.Version), "tb_entity", entity.ID, entity); e != nil {
			return e
		}
		if e := t.Enqueue(fmt.Sprintf("sync-entity:%s:%d", entity.ID, entity.Version), "entity_sync", entity.EdgeID, entity); e != nil {
			return e
		}
		if s.Mode == "edge" && entity.Kind == "device" {
			if e := t.Enqueue(fmt.Sprintf("device-config:%s:%d", entity.ID, entity.Version), "device_config", entity.ID, entity); e != nil {
				return e
			}
		}
		return t.Audit(p.Actor, "entity.update", entity.ID, "", entity)
	})
	if e == nil {
		respond(w, 200, entity)
	}
	return e
}
func has(v []string, s string) bool {
	for _, x := range v {
		if x == s {
			return true
		}
	}
	return false
}
func (s *Server) query(r *http.Request, p identity.Principal) (model.DataResult, error) {
	windowMS := number(r.URL.Query().Get("window_ms"), 3600000)
	if windowMS < 1000 || windowMS > 3*366*24*3600000 {
		return model.DataResult{}, APIError{400, "window_ms must be between 1000 and three years"}
	}
	opts := store.Query{FromMS: number(r.URL.Query().Get("from_ms"), s.Store.Now().UnixMilli()-windowMS), ToMS: number(r.URL.Query().Get("to_ms"), s.Store.Now().UnixMilli()), Limit: int(number(r.URL.Query().Get("limit"), 2000)), Resolution: r.URL.Query().Get("resolution"), IncludeRevisions: true}
	if v := r.URL.Query().Get("device_ids"); v != "" {
		opts.DeviceIDs = strings.Split(v, ",")
	}
	if v := r.URL.Query().Get("keys"); v != "" {
		opts.Keys = strings.Split(v, ",")
	}
	if len(opts.DeviceIDs) == 0 {
		entities, e := s.visibleEntities(r, p)
		if e != nil {
			return model.DataResult{}, e
		}
		for _, entity := range entities {
			opts.DeviceIDs = append(opts.DeviceIDs, entity.ID)
		}
		if len(opts.DeviceIDs) == 0 {
			return model.DataResult{Points: []model.Observation{}, Sources: []model.SourceState{}, Revisions: []model.Revision{}, Quality: model.QualitySummary{Completeness: "unknown"}}, nil
		}
	}
	for _, id := range opts.DeviceIDs {
		if e := s.allow(r, p, "read", id); e != nil {
			return model.DataResult{}, e
		}
	}
	result, e := s.Store.Query(r.Context(), opts)
	if e != nil {
		return result, e
	}
	result.Sources = s.filterSources(r, p, result.Sources)
	if r.URL.Query().Get("include_latest") == "true" {
		result.Latest, e = s.Store.LatestValues(r.Context(), opts.DeviceIDs, opts.Keys)
		if e != nil {
			return result, e
		}
		for _, point := range result.Latest {
			result.DataVersion = max(result.DataVersion, point.ReceivedMS)
		}
	}
	return result, nil
}
func (s *Server) filterSources(r *http.Request, p identity.Principal, sources []model.SourceState) []model.SourceState {
	out := []model.SourceState{}
	for _, source := range sources {
		if s.allow(r, p, "read", source.ID) == nil {
			out = append(out, source)
		}
	}
	return out
}
func number(v string, fallback int64) int64 {
	if v == "" {
		return fallback
	}
	n, e := strconv.ParseInt(v, 10, 64)
	if e != nil {
		return fallback
	}
	return n
}
func (s *Server) data(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	out, e := s.query(r, p)
	if e == nil {
		respond(w, 200, out)
	}
	return e
}
func (s *Server) ingest(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	var batch store.IngestBatch
	if e := decode(r, &batch); e != nil {
		return e
	}
	batch.PayloadHash = ""
	devices := []string{}
	seen := map[string]bool{}
	for _, gap := range batch.Gaps {
		if e := s.allow(r, p, "ingest", gap.DeviceID); e != nil {
			return e
		}
	}
	for _, point := range batch.Points {
		if seen[point.DeviceID] {
			continue
		}
		seen[point.DeviceID] = true
		if e := s.allow(r, p, "ingest", point.DeviceID); e != nil {
			return e
		}
		devices = append(devices, point.DeviceID)
	}
	documents, e := s.Store.GetMany(r.Context(), "entity", devices)
	if e != nil {
		return e
	}
	for _, device := range devices {
		doc, ok := documents[device]
		if !ok {
			return store.ErrNotFound
		}
		entity, e := store.Decode[model.Entity](doc)
		if e != nil {
			return e
		}
		if entity.Kind != "device" || entity.Status != "approved" {
			return errors.New("device is not approved")
		}
	}
	result, e := s.Store.Ingest(r.Context(), batch)
	if e == nil {
		respond(w, 200, result)
	}
	return e
}
func (s *Server) events(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	if _, e := s.query(r, p); e != nil {
		return e
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		return errors.New("streaming is unavailable")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		result, e := s.query(r, p)
		if e != nil {
			b, _ := json.Marshal(map[string]string{"error": e.Error()})
			fmt.Fprintf(w, "event: error\ndata: %s\n\n", b)
			flusher.Flush()
			return nil
		}
		b, e := json.Marshal(result)
		if e != nil {
			return e
		}
		if _, e = fmt.Fprintf(w, "event: data\ndata: %s\n\n", b); e != nil {
			return nil
		}
		flusher.Flush()
		select {
		case <-r.Context().Done():
			return nil
		case <-ticker.C:
			current, e := s.Identity.Authenticate(r.Context(), bearer(r))
			if e != nil {
				return nil
			}
			p = current
		}
	}
}
