package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"competition2026/product/platform/internal/configcenter"
	"competition2026/product/platform/internal/engine"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/plugins"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func (s *Server) visibleDocuments(r *http.Request, p identity.Principal, kind string) ([]store.Document, error) {
	docs, e := s.Store.List(r.Context(), kind)
	if e != nil {
		return nil, e
	}
	out := []store.Document{}
	for _, d := range docs {
		var obj map[string]any
		if e = store.DecodeJSON(d.Data, &obj); e != nil {
			return nil, e
		}
		resource, _ := obj["group_id"].(string)
		if resource == "" {
			resource, _ = obj["entity_id"].(string)
		}
		if resource == "" {
			resource, _ = obj["device_id"].(string)
		}
		if kind == "draft" {
			draft, e := store.Decode[model.Draft](d)
			if e != nil {
				return nil, e
			}
			resource = draft.Definition.GroupID
		}
		if resource == "" {
			if defID, ok := obj["definition_id"].(string); ok {
				def, e := s.Engine.Published(r.Context(), defID, 0)
				if e != nil {
					return nil, e
				}
				resource = def.GroupID
			}
		}
		if resource == "" {
			resource = "*"
		}
		if s.allow(r, p, "read", resource) == nil {
			out = append(out, d)
		}
	}
	return out, nil
}
func outputDocuments(w http.ResponseWriter, docs []store.Document) {
	out := []json.RawMessage{}
	for _, d := range docs {
		out = append(out, d.Data)
	}
	respond(w, 200, out)
}
func (s *Server) catalogue(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	docs, e := s.visibleDocuments(r, p, "catalogue")
	if e == nil {
		outputDocuments(w, docs)
	}
	return e
}
func (s *Server) definitions(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	docs, e := s.visibleDocuments(r, p, "definition")
	if e == nil {
		outputDocuments(w, docs)
	}
	return e
}
func (s *Server) definitionVersions(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	def, e := s.Engine.Published(r.Context(), r.PathValue("id"), 0)
	if e != nil {
		return e
	}
	if e = s.allow(r, p, "read", def.GroupID); e != nil {
		return e
	}
	docs, e := s.Store.Versions(r.Context(), "definition", def.ID)
	if e == nil {
		outputDocuments(w, docs)
	}
	return e
}
func (s *Server) drafts(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	docs, e := s.visibleDocuments(r, p, "draft")
	if e == nil {
		outputDocuments(w, docs)
	}
	return e
}
func (s *Server) saveDraft(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	var req struct {
		Draft           model.Draft `json:"draft"`
		ExpectedVersion int64       `json:"expected_version"`
	}
	if e := decode(r, &req); e != nil {
		return e
	}
	if e := s.definitionAccess(r, p, req.Draft.Definition, "draft"); e != nil {
		return e
	}
	if req.ExpectedVersion < 0 || req.Draft.BaseVersion < 0 {
		return APIError{400, "nonnegative versions are required"}
	}
	if req.Draft.ID == "" {
		req.Draft.ID = req.Draft.Definition.ID
	}
	if current, e := s.Engine.Published(r.Context(), req.Draft.Definition.ID, 0); e == nil {
		if e = s.allow(r, p, "draft", current.GroupID); e != nil {
			return e
		}
	} else if !errors.Is(e, store.ErrNotFound) {
		return e
	}
	if d, e := s.Store.Get(r.Context(), "draft", req.Draft.ID); e == nil {
		old, e := store.Decode[model.Draft](d)
		if e != nil {
			return e
		}
		if e = s.allow(r, p, "draft", old.Definition.GroupID); e != nil {
			return e
		}
	}
	draft, e := s.Engine.SaveDraft(r.Context(), p.Actor, req.Draft, req.ExpectedVersion)
	if e == nil {
		respond(w, 200, draft)
	}
	return e
}
func (s *Server) draft(r *http.Request, p identity.Principal, action string) (model.Draft, error) {
	doc, e := s.Store.Get(r.Context(), "draft", r.PathValue("id"))
	if e != nil {
		return model.Draft{}, e
	}
	draft, e := store.Decode[model.Draft](doc)
	if e != nil {
		return draft, e
	}
	e = s.definitionAccess(r, p, draft.Definition, action)
	return draft, e
}
func (s *Server) definitionAccess(r *http.Request, p identity.Principal, d model.Definition, action string) error {
	if d.GroupID == "" {
		return identity.ErrDenied
	}
	if e := s.allow(r, p, action, d.GroupID); e != nil {
		return e
	}
	if len(d.Selector.DeviceIDs) == 0 && d.Selector.AssetID == "" {
		return APIError{400, "select at least one device or an asset hierarchy"}
	}
	resources := append([]string{}, d.Selector.DeviceIDs...)
	if d.Selector.AssetID != "" {
		resources = append(resources, d.Selector.AssetID)
	}
	for _, c := range d.Policy.Conditions {
		resources = append(resources, c.DeviceID)
	}
	for _, step := range append(append([]model.Step{}, d.Policy.Steps...), d.Policy.Degraded...) {
		resources = append(resources, step.DeviceID)
	}
	for _, id := range resources {
		if id == "" {
			return identity.ErrDenied
		}
		if e := s.allow(r, p, "read", id); e != nil {
			return e
		}
	}
	for _, id := range d.Dependencies {
		other, e := s.Engine.Published(r.Context(), id, 0)
		if e != nil {
			return e
		}
		if e = s.allow(r, p, "read", other.GroupID); e != nil {
			return e
		}
	}
	return nil
}
func (s *Server) validateDraft(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	draft, e := s.draft(r, p, "draft")
	if e != nil {
		return e
	}
	respond(w, 200, s.Engine.Validate(r.Context(), draft.Definition))
	return nil
}
func (s *Server) draftDiff(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	draft, e := s.draft(r, p, "read")
	if e != nil {
		return e
	}
	previous, e := s.Engine.Published(r.Context(), draft.Definition.ID, 0)
	if e != nil && !errors.Is(e, store.ErrNotFound) {
		return e
	}
	respond(w, 200, engine.Diff(previous, draft.Definition))
	return nil
}
func (s *Server) simulateDraft(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	draft, e := s.draft(r, p, "draft")
	if e != nil {
		return e
	}
	var req struct {
		Point model.Observation `json:"point"`
	}
	if e = decode(r, &req); e != nil {
		return e
	}
	if e = s.allow(r, p, "read", req.Point.DeviceID); e != nil {
		return e
	}
	result, e := s.Engine.Simulate(r.Context(), draft.Definition, req.Point)
	if e == nil {
		respond(w, 200, result)
	}
	return e
}
func (s *Server) publishDraft(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	if s.Mode == "edge" {
		return errors.New("new definitions are published in the cloud")
	}
	draft, e := s.draft(r, p, "publish")
	if e != nil {
		return e
	}
	definition, e := s.Engine.Publish(r.Context(), p.Actor, draft.ID)
	if e == nil {
		respond(w, 200, definition)
	}
	return e
}
func (s *Server) deactivate(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	if s.Mode == "edge" {
		return errors.New("definitions are deactivated in the cloud")
	}
	d, e := s.Engine.Published(r.Context(), r.PathValue("id"), 0)
	if e != nil {
		return e
	}
	if e = s.allow(r, p, "publish", d.GroupID); e != nil {
		return e
	}
	var req struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if e = decode(r, &req); e != nil {
		return e
	}
	out, e := s.Engine.Deactivate(r.Context(), p.Actor, d.ID, req.ExpectedVersion)
	if e == nil {
		respond(w, 200, out)
	}
	return e
}
func (s *Server) rollback(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	current, e := s.Engine.Published(r.Context(), r.PathValue("id"), 0)
	if e != nil {
		return e
	}
	if e = s.allow(r, p, "draft", current.GroupID); e != nil {
		return e
	}
	var req struct {
		Version int64 `json:"version"`
	}
	if e = decode(r, &req); e != nil {
		return e
	}
	docs, e := s.Store.Versions(r.Context(), "definition", current.ID)
	if e != nil {
		return e
	}
	for _, doc := range docs {
		old, e := store.Decode[model.Definition](doc)
		if e != nil {
			return e
		}
		if old.Version == req.Version {
			draft, e := s.Engine.SaveDraft(r.Context(), p.Actor, model.Draft{ID: identity.ID(), Definition: old, BaseVersion: current.Version}, 0)
			if e == nil {
				respond(w, 200, draft)
			}
			return e
		}
	}
	return store.ErrNotFound
}
func (s *Server) executions(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	docs, e := s.visibleDocuments(r, p, "execution")
	if e == nil {
		outputDocuments(w, docs)
	}
	return e
}
func (s *Server) createExecution(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	var req struct {
		DefinitionID string            `json:"definition_id"`
		Params       map[string]string `json:"params"`
		Override     bool              `json:"override"`
		DownlinkID   string            `json:"downlink_id"`
	}
	if e := decode(r, &req); e != nil {
		return e
	}
	out, e := s.Control.Create(r.Context(), p, req.DefinitionID, req.Params, req.Override, req.DownlinkID)
	if e == nil {
		respond(w, 200, out)
	}
	return e
}
func (s *Server) approve(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	var req struct {
		Role string `json:"role"`
	}
	if e := decode(r, &req); e != nil {
		return e
	}
	out, e := s.Control.Approve(r.Context(), p, r.PathValue("id"), req.Role)
	if e == nil {
		respond(w, 200, out)
	}
	return e
}
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	out, e := s.Control.Dispatch(r.Context(), p, r.PathValue("id"))
	if e == nil {
		respond(w, 200, out)
	}
	return e
}
func (s *Server) alarms(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	docs, e := s.visibleDocuments(r, p, "alarm")
	if e == nil {
		outputDocuments(w, docs)
	}
	return e
}
func (s *Server) ackAlarm(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	doc, e := s.Store.Get(r.Context(), "alarm", r.PathValue("id"))
	if e != nil {
		return e
	}
	alarm, e := store.Decode[model.Alarm](doc)
	if e != nil {
		return e
	}
	if e = s.allow(r, p, "approve", alarm.EntityID); e != nil {
		return e
	}
	alarm.Acknowledged = true
	alarm.Version++
	e = s.Store.Write(r.Context(), func(t *store.Tx) error {
		if _, e := t.Put("alarm", alarm.ID, doc.Version, alarm); e != nil {
			return e
		}
		activeID := alarm.DefinitionID + ":" + alarm.EntityID
		if current, err := t.Get("active_alarm", activeID); err == nil {
			active, err := store.Decode[model.Alarm](current)
			if err != nil {
				return err
			}
			if active.ID == alarm.ID {
				if _, err = t.Put("active_alarm", activeID, current.Version, alarm); err != nil {
					return err
				}
			}
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if e := t.Enqueue(fmt.Sprintf("tb-alarm:%s:%d", alarm.ID, alarm.Version), "tb_alarm", alarm.EntityID, alarm); e != nil {
			return e
		}
		return t.Audit(p.Actor, "alarm.acknowledge", alarm.EntityID, alarm.ID, alarm)
	})
	if e == nil {
		respond(w, 200, alarm)
	}
	return e
}
func (s *Server) jobs(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	docs, e := s.visibleDocuments(r, p, "job")
	if e == nil {
		outputDocuments(w, docs)
	}
	return e
}
func (s *Server) organization(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	result := map[string]any{}
	for _, kind := range []string{"department", "user", "grant"} {
		docs, e := s.Store.List(r.Context(), kind)
		if e != nil {
			return e
		}
		out := []json.RawMessage{}
		for _, d := range docs {
			out = append(out, d.Data)
		}
		result[kind+"s"] = out
	}
	respond(w, 200, result)
	return nil
}
func (s *Server) syncOrganization(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	if s.Mode == "edge" {
		return identity.ErrDenied
	}
	var batch plugins.OrganizationSync
	if e := decode(r, &batch); e != nil {
		return e
	}
	if e := plugins.SyncOrganization(r.Context(), s.Store, p.Actor, batch); e != nil {
		return e
	}
	respond(w, 200, map[string]bool{"committed": true})
	return nil
}
func (s *Server) putUser(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	if s.Mode == "edge" {
		return identity.ErrDenied
	}
	var req struct {
		User            model.User `json:"user"`
		Password        string     `json:"password"`
		TOTPSecret      string     `json:"totp_secret"`
		ExpectedVersion int64      `json:"expected_version"`
	}
	if e := decode(r, &req); e != nil {
		return e
	}
	u, e := s.Identity.CreateUser(r.Context(), p.Actor, req.User, req.Password, req.TOTPSecret, req.ExpectedVersion)
	if e == nil {
		respond(w, 200, u)
	}
	return e
}
func (s *Server) putGrant(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	if s.Mode == "edge" {
		return identity.ErrDenied
	}
	var req struct {
		Grant           identity.Grant `json:"grant"`
		ExpectedVersion int64          `json:"expected_version"`
	}
	if e := decode(r, &req); e != nil {
		return e
	}
	if req.Grant.ID == "" {
		req.Grant.ID = identity.ID()
	}
	if req.Grant.TeamID == "" || req.Grant.GroupID == "" {
		return errors.New("team_id and group_id are required")
	}
	e := s.Store.Write(r.Context(), func(t *store.Tx) error {
		if _, e := t.Put("grant", req.Grant.ID, req.ExpectedVersion, req.Grant); e != nil {
			return e
		}
		return t.Audit(p.Actor, "identity.grant.update", req.Grant.GroupID, "", req.Grant)
	})
	if e == nil {
		respond(w, 200, req.Grant)
	}
	return e
}
func (s *Server) proxyConfig(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	url := strings.TrimRight(s.ConfigURL, "/") + r.URL.RequestURI()
	req, e := http.NewRequestWithContext(r.Context(), r.Method, url, r.Body)
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.ServiceToken)
	actor, _ := json.Marshal(p.Actor)
	req.Header.Set("X-SF-Actor", base64.StdEncoding.EncodeToString(actor))
	client := s.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	response, e := client.Do(req)
	if e != nil {
		return APIError{503, "configuration service is unavailable"}
	}
	defer response.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.StatusCode)
	_, e = io.Copy(w, io.LimitReader(response.Body, 2<<20))
	return e
}
func (s *Server) listConfig(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	if s.ConfigURL != "" && s.Mode != "config" {
		return s.proxyConfig(w, r, p)
	}
	out, e := s.Config.List(r.Context())
	if e == nil {
		respond(w, 200, out)
	}
	return e
}
func (s *Server) putConfig(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	if s.ConfigURL != "" && s.Mode != "config" {
		return s.proxyConfig(w, r, p)
	}
	var req struct {
		Parameter       configcenter.Parameter `json:"parameter"`
		ExpectedVersion int64                  `json:"expected_version"`
	}
	if e := decode(r, &req); e != nil {
		return e
	}
	out, e := s.Config.Put(r.Context(), p.Actor, req.Parameter, req.ExpectedVersion)
	if e == nil {
		respond(w, 200, out)
	}
	return e
}
func (s *Server) ackConfig(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	if s.ConfigURL != "" && s.Mode != "config" {
		return s.proxyConfig(w, r, p)
	}
	var req struct {
		NodeID  string `json:"node_id"`
		Version int64  `json:"version"`
		Applied bool   `json:"applied"`
		Reason  string `json:"reason"`
	}
	if e := decode(r, &req); e != nil {
		return e
	}
	if e := s.Config.Acknowledge(r.Context(), req.NodeID, r.PathValue("id"), req.Version, req.Applied, req.Reason); e != nil {
		return e
	}
	respond(w, 200, map[string]bool{"ok": true})
	return nil
}
func (s *Server) audit(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	out, e := s.Store.AuditList(r.Context(), r.URL.Query().Get("request_id"), 1000)
	if e == nil {
		respond(w, 200, out)
	}
	return e
}
func (s *Server) verifyAudit(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	issues, e := s.Store.VerifyAudit(r.Context())
	if e == nil {
		respond(w, 200, map[string]any{"valid": len(issues) == 0, "issues": issues})
	}
	return e
}
func (s *Server) runtime(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	sources, e := s.Store.Sources(r.Context())
	if e != nil {
		return e
	}
	type queue struct {
		Kind     string `json:"kind"`
		Count    int64  `json:"count"`
		OldestMS int64  `json:"oldest_ms"`
	}
	rows, e := s.Store.DB.QueryContext(r.Context(), "SELECT kind,count(*),min(created_ms) FROM outbox GROUP BY kind ORDER BY kind")
	if e != nil {
		return e
	}
	defer rows.Close()
	queues := []queue{}
	for rows.Next() {
		var q queue
		if e = rows.Scan(&q.Kind, &q.Count, &q.OldestMS); e != nil {
			return e
		}
		queues = append(queues, q)
	}
	respond(w, 200, map[string]any{"sources": s.filterSources(r, p, sources), "queues": queues, "node_id": s.NodeID, "mode": s.Mode, "time_ms": s.Store.Now().UnixMilli()})
	return rows.Err()
}

// CallAPI routes assistant tools through the same HTTP authorization checks.
func (s *Server) CallAPI(r *http.Request, method, path string, payload any) (any, error) {
	var body io.Reader
	if payload != nil {
		b, e := json.Marshal(payload)
		if e != nil {
			return nil, e
		}
		body = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(r.Context(), method, path, body)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", r.Header.Get("Authorization"))
	rec := &responseBuffer{HeaderMap: http.Header{}}
	s.Handler().ServeHTTP(rec, req)
	var value any
	if e = store.DecodeJSON(rec.Body.Bytes(), &value); e != nil {
		return nil, e
	}
	if rec.Status >= 400 {
		return nil, APIError{rec.Status, rec.Body.String()}
	}
	return value, nil
}

type responseBuffer struct {
	HeaderMap http.Header
	Body      bytes.Buffer
	Status    int
}

func (r *responseBuffer) Header() http.Header    { return r.HeaderMap }
func (r *responseBuffer) WriteHeader(status int) { r.Status = status }
func (r *responseBuffer) Write(p []byte) (int, error) {
	if r.Status == 0 {
		r.Status = 200
	}
	return r.Body.Write(p)
}
