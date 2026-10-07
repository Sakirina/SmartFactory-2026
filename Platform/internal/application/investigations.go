package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"competition2026/product/platform/internal/control"
	"competition2026/product/platform/internal/historymodel"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/observability"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type InvestigationRepository interface {
	DefinitionRepository
	Investigation(context.Context, string) (model.Investigation, error)
	InvestigationIDs(context.Context, string, string, int) ([]string, error)
	InvestigationEvidence(context.Context, string) (model.InvestigationEvidence, error)
	InvestigationEvidenceList(context.Context, string) ([]model.InvestigationEvidence, error)
	AIDocuments(context.Context, string, string, int) ([]store.Document, error)
}

type Investigations struct {
	Store       InvestigationRepository
	Identity    *identity.Manager
	Definitions *Definitions
	Control     *control.Service
	History     *History
	Business    *Business
	TTL         time.Duration
}

const AIResultBudget = 512 << 10
const AIDocumentScanBudget = 1000

func (s *Investigations) authorize(ctx context.Context, p identity.Principal) (*revisions, error) {
	return s.Business.authorize(ctx, p, "read", nil)
}

func (s *Investigations) owned(ctx context.Context, p identity.Principal, id string) (model.Investigation, *revisions, error) {
	seen, err := s.authorize(ctx, p)
	if err != nil {
		return model.Investigation{}, nil, err
	}
	value, err := s.Store.Investigation(ctx, id)
	if err != nil {
		return value, nil, err
	}
	if value.UserID != p.User.ID {
		return model.Investigation{}, nil, store.ErrNotFound
	}
	return value, seen, nil
}

func (s *Investigations) Begin(ctx context.Context, p identity.Principal, provider, api, modelName string, messages []model.InvestigationMessage) (_ model.Investigation, operationErr error) {
	ctx, finish := observability.StartOperation(ctx, "ai.investigation.begin", observability.Identity{ActorID: p.User.ID})
	defer func() { finish(operationErr) }()
	seen, err := s.authorize(ctx, p)
	if err != nil {
		return model.Investigation{}, err
	}
	ttl := s.TTL
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	now := s.Store.CurrentTime().UnixMilli()
	value := model.Investigation{ID: identity.ID(), UserID: p.User.ID, Version: 1, Provider: provider, API: api, Model: modelName, Messages: messages, Status: "running", CreatedMS: now, UpdatedMS: now, ExpiresMS: now + ttl.Milliseconds(), EvidenceStatus: "pending", References: []model.EvidenceReference{}, Evidence: []model.InvestigationEvidence{}}
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("ai.investigation_id", value.ID))
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		if err := seen.check(tx); err != nil {
			return err
		}
		if err := tx.PutInvestigation(value, 0); err != nil {
			return err
		}
		return tx.Audit(p.Actor, "ai.investigation.begin", value.ID, "", map[string]any{"provider": provider, "api": api, "model": modelName})
	})
	return value, err
}

func (s *Investigations) Record(ctx context.Context, p identity.Principal, e model.InvestigationEvidence) (_ model.InvestigationEvidence, operationErr error) {
	ctx, finish := observability.StartOperation(ctx, "ai.investigation.evidence", observability.Identity{ActorID: p.User.ID})
	defer func() { finish(operationErr) }()
	value, seen, err := s.owned(ctx, p, e.InvestigationID)
	if err != nil {
		return e, err
	}
	if value.Status != "running" {
		return e, store.ErrConflict
	}
	if e.ID == "" || e.ToolCallID == "" || e.ToolName == "" || e.VisibleContent == "" || len(e.VisibleContent) > AIResultBudget {
		return e, errors.New("invalid model-visible evidence")
	}
	var visible any
	if err = store.DecodeJSON([]byte(e.VisibleContent), &visible); err != nil {
		return e, err
	}
	if e.CreatedMS == 0 {
		e.CreatedMS = s.Store.CurrentTime().UnixMilli()
	}
	e.ExpiresMS = value.ExpiresMS
	e.Delivery = "prepared"
	e.VisibleBytes = len(e.VisibleContent)
	e.BudgetBytes = AIResultBudget
	sum := sha256.Sum256([]byte(e.VisibleContent))
	e.VisibleSHA256 = hex.EncodeToString(sum[:])
	all, err := s.Store.InvestigationEvidenceList(ctx, value.ID)
	if err != nil {
		return e, err
	}
	for _, prior := range all {
		if prior.ToolCallID == e.ToolCallID {
			return e, errors.New("model reused a tool call identity")
		}
	}
	e.Ordinal = len(all) + 1
	value.Version++
	value.UpdatedMS = e.CreatedMS
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("ai.investigation_id", value.ID), attribute.String("ai.evidence_id", e.ID), attribute.String("ai.tool_call_id", e.ToolCallID), attribute.String("ai.tool_name", e.ToolName), attribute.String("ai.evidence_status", e.Status), attribute.Int("ai.visible_bytes", e.VisibleBytes))
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		if err := seen.check(tx); err != nil {
			return err
		}
		if err := tx.PutInvestigation(value, value.Version-1); err != nil {
			return err
		}
		if err := tx.PutInvestigationEvidence(e, true); err != nil {
			return err
		}
		return tx.Audit(p.Actor, "ai.investigation.evidence", value.ID, e.ID, map[string]any{"tool_call_id": e.ToolCallID, "tool_name": e.ToolName, "status": e.Status, "visible_sha256": e.VisibleSHA256, "visible_bytes": e.VisibleBytes})
	})
	return e, err
}

// Delivered is called after the subsequent model request has completed. Prepared
// results from a failed or cancelled continuation cannot validate citations.
func (s *Investigations) Delivered(ctx context.Context, p identity.Principal, id string, evidenceIDs []string) error {
	if len(evidenceIDs) == 0 {
		return nil
	}
	value, seen, err := s.owned(ctx, p, id)
	if err != nil {
		return err
	}
	if value.Status != "running" {
		return store.ErrConflict
	}
	items := []model.InvestigationEvidence{}
	for _, eid := range evidenceIDs {
		e, err := s.Store.InvestigationEvidence(ctx, eid)
		if err != nil {
			return err
		}
		if e.InvestigationID != id {
			return store.ErrConflict
		}
		e.Delivery = "delivered"
		items = append(items, e)
	}
	value.Version++
	value.UpdatedMS = s.Store.CurrentTime().UnixMilli()
	return s.Store.Write(ctx, func(tx *store.Tx) error {
		if err := seen.check(tx); err != nil {
			return err
		}
		if err := tx.PutInvestigation(value, value.Version-1); err != nil {
			return err
		}
		for _, e := range items {
			if err := tx.PutInvestigationEvidence(e, false); err != nil {
				return err
			}
		}
		return nil
	})
}

var evidenceCitation = regexp.MustCompile(`\[evidence:([^\]\r\n]{1,200})\]`)

func (s *Investigations) reference(ctx context.Context, p identity.Principal, investigation, id string) model.EvidenceReference {
	ref := model.EvidenceReference{ID: id, Status: "unknown"}
	e, err := s.Store.InvestigationEvidence(ctx, id)
	if err != nil {
		return ref
	}
	if e.InvestigationID != investigation {
		ref.Status = "cross_investigation"
		return ref
	}
	ref.URL = "/api/sf/v1/investigations/" + url.PathEscape(investigation) + "/evidence/" + url.PathEscape(id)
	ref.Status = s.evidenceState(ctx, p, e)
	return ref
}

func (s *Investigations) evidenceState(ctx context.Context, p identity.Principal, e model.InvestigationEvidence) string {
	if e.ExpiresMS <= s.Store.CurrentTime().UnixMilli() {
		return "expired"
	}
	if e.VisibleContent == "" {
		return "deleted"
	}
	if e.Delivery != "delivered" {
		return "not_delivered"
	}
	for _, r := range e.Resources {
		if err := s.checkResource(ctx, p, r); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return "deleted"
			}
			return "forbidden"
		}
	}
	switch e.Status {
	case "error":
		return "tool_error"
	case "budget_exceeded":
		return "budget_exceeded"
	case "empty":
		return "empty"
	}
	return "valid"
}

func (s *Investigations) References(ctx context.Context, p identity.Principal, id, answer string) ([]model.EvidenceReference, string) {
	out := []model.EvidenceReference{}
	seen := map[string]bool{}
	valid := 0
	for _, match := range evidenceCitation.FindAllStringSubmatch(answer, -1) {
		if seen[match[1]] {
			continue
		}
		seen[match[1]] = true
		ref := s.reference(ctx, p, id, match[1])
		out = append(out, ref)
		if ref.Status == "valid" {
			valid++
		}
	}
	status := "missing"
	if len(out) > 0 {
		status = "unsupported"
		if valid == len(out) {
			status = "supported"
		} else if valid > 0 {
			status = "partial"
		}
	}
	return out, status
}

func (s *Investigations) Complete(ctx context.Context, p identity.Principal, id, answer string) (_ model.Investigation, operationErr error) {
	ctx, finish := observability.StartOperation(ctx, "ai.investigation.complete", observability.Identity{ActorID: p.User.ID})
	defer func() { finish(operationErr) }()
	value, seen, err := s.owned(ctx, p, id)
	if err != nil {
		return value, err
	}
	if value.Status != "running" {
		return value, store.ErrConflict
	}
	value.Answer = answer
	value.Status = "completed"
	value.References, value.EvidenceStatus = s.References(ctx, p, id, answer)
	value.Version++
	value.UpdatedMS = s.Store.CurrentTime().UnixMilli()
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		if err := seen.check(tx); err != nil {
			return err
		}
		if err := tx.PutInvestigation(value, value.Version-1); err != nil {
			return err
		}
		return tx.Audit(p.Actor, "ai.investigation.complete", value.ID, "", map[string]any{"evidence_status": value.EvidenceStatus, "references": value.References})
	})
	if err != nil {
		return value, err
	}
	return s.Get(ctx, p, id)
}

// Fail records termination even when the request's session has been revoked.
// It is an internal lifecycle operation, never an HTTP mutation entry point.
func (s *Investigations) Fail(ctx context.Context, id, user, status, reason string) error {
	value, err := s.Store.Investigation(ctx, id)
	if err != nil {
		return err
	}
	if value.UserID != user || value.Status != "running" {
		return store.ErrConflict
	}
	if status != "failed" && status != "cancelled" {
		return errors.New("invalid termination status")
	}
	value.Status = status
	value.Error = reason
	value.EvidenceStatus = "unsupported"
	value.Version++
	value.UpdatedMS = s.Store.CurrentTime().UnixMilli()
	return s.Store.Write(ctx, func(tx *store.Tx) error { return tx.PutInvestigation(value, value.Version-1) })
}

func (s *Investigations) Get(ctx context.Context, p identity.Principal, id string) (model.Investigation, error) {
	value, _, err := s.owned(ctx, p, id)
	if err != nil {
		return value, err
	}
	value.Evidence, err = s.Store.InvestigationEvidenceList(ctx, id)
	if err != nil {
		return value, err
	}
	value.References, value.EvidenceStatus = s.References(ctx, p, id, value.Answer)
	if value.Status == "running" && len(value.References) == 0 {
		value.EvidenceStatus = "pending"
	}
	if value.Status == "failed" || value.Status == "cancelled" {
		value.EvidenceStatus = "unsupported"
	}
	clearAnswer := value.Status == "deleted" || value.ExpiresMS <= s.Store.CurrentTime().UnixMilli()
	for i, e := range value.Evidence {
		state := s.evidenceState(ctx, p, e)
		value.Evidence[i].AccessStatus = state
		value.Evidence[i].VisibleContent = ""
		if state == "forbidden" || state == "deleted" || state == "expired" {
			value.Evidence[i].Arguments = nil
			value.Evidence[i].Resources = []model.EvidenceResource{}
			clearAnswer = true
		}
	}
	if clearAnswer {
		value.Answer = ""
		value.Messages = []model.InvestigationMessage{}
		if value.Status != "running" {
			value.EvidenceStatus = "unsupported"
		}
	}
	return value, nil
}

func (s *Investigations) Evidence(ctx context.Context, p identity.Principal, id, eid string) (model.InvestigationEvidence, error) {
	value, _, err := s.owned(ctx, p, id)
	if err != nil {
		return model.InvestigationEvidence{}, err
	}
	e, err := s.Store.InvestigationEvidence(ctx, eid)
	if err != nil {
		return e, err
	}
	if e.InvestigationID != id {
		return model.InvestigationEvidence{}, store.ErrNotFound
	}
	state := s.evidenceState(ctx, p, e)
	if value.Status == "deleted" || state == "deleted" {
		return model.InvestigationEvidence{}, store.ErrNotFound
	}
	if state == "expired" {
		return model.InvestigationEvidence{}, store.ErrQueryExpired
	}
	if state == "forbidden" {
		return model.InvestigationEvidence{}, identity.ErrDenied
	}
	e.AccessStatus = state
	return e, nil
}

func (s *Investigations) List(ctx context.Context, p identity.Principal, after string, limit int) (model.InvestigationList, error) {
	if _, err := s.authorize(ctx, p); err != nil {
		return model.InvestigationList{}, err
	}
	if limit < 1 || limit > 100 {
		return model.InvestigationList{}, errors.New("limit must be between 1 and 100")
	}
	ids, err := s.Store.InvestigationIDs(ctx, p.User.ID, after, limit+1)
	if err != nil {
		return model.InvestigationList{}, err
	}
	out := model.InvestigationList{Items: []model.Investigation{}, HasMore: len(ids) > limit}
	if out.HasMore {
		ids = ids[:limit]
	}
	for _, id := range ids {
		v, err := s.Get(ctx, p, id)
		if err != nil {
			return out, err
		}
		v.Messages = []model.InvestigationMessage{}
		v.Evidence = []model.InvestigationEvidence{}
		v.Answer = ""
		out.Items = append(out.Items, v)
		out.NextAfter = id
	}
	if !out.HasMore {
		out.NextAfter = ""
	}
	return out, nil
}

func (s *Investigations) Delete(ctx context.Context, p identity.Principal, id string) error {
	value, seen, err := s.owned(ctx, p, id)
	if err != nil {
		return err
	}
	if value.Status == "running" {
		return store.ErrConflict
	}
	if value.Status == "deleted" {
		return nil
	}
	all, err := s.Store.InvestigationEvidenceList(ctx, id)
	if err != nil {
		return err
	}
	value.Status = "deleted"
	value.Answer = ""
	value.Messages = []model.InvestigationMessage{}
	value.Error = ""
	value.Version++
	value.UpdatedMS = s.Store.CurrentTime().UnixMilli()
	return s.Store.Write(ctx, func(tx *store.Tx) error {
		if err := seen.check(tx); err != nil {
			return err
		}
		if err := tx.PutInvestigation(value, value.Version-1); err != nil {
			return err
		}
		for _, e := range all {
			e.VisibleContent = ""
			e.Arguments = nil
			e.Resources = []model.EvidenceResource{}
			if err := tx.PutInvestigationEvidence(e, false); err != nil {
				return err
			}
		}
		return tx.Audit(p.Actor, "ai.investigation.delete", id, "", map[string]any{"version": value.Version})
	})
}

func (s *Investigations) ReadDefinition(ctx context.Context, p identity.Principal, id string) (model.Definition, error) {
	value, err := s.ReadDefinitionEvidence(ctx, p, id)
	if err != nil {
		return model.Definition{}, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return model.Definition{}, err
	}
	var definition model.Definition
	err = store.DecodeJSON(raw, &definition)
	return definition, err
}

func (s *Investigations) ReadCatalogue(ctx context.Context, p identity.Principal, id string) (any, error) {
	doc, err := s.Store.Get(ctx, "catalogue", id)
	if err != nil {
		return nil, err
	}
	return s.readAIDocument(ctx, p, doc)
}

func (s *Investigations) checkResource(ctx context.Context, p identity.Principal, r model.EvidenceResource) error {
	switch r.Kind {
	case "definition":
		_, err := s.ReadDefinition(ctx, p, r.ID)
		return err
	case "draft":
		_, err := s.Definitions.LoadDraft(ctx, p, DraftInput{ID: r.ID}, "read")
		return err
	case "execution":
		_, err := s.Control.Detail(ctx, p, r.ID)
		return err
	case "analysis_run":
		_, err := s.History.Get(ctx, p, r.ID)
		return err
	case "alarm":
		doc, err := s.Store.Get(ctx, "alarm", r.ID)
		if err != nil {
			return err
		}
		alarm, err := store.Decode[model.Alarm](doc)
		if err != nil {
			return err
		}
		return s.Identity.Permit(ctx, p, "read", alarm.EntityID)
	case "catalogue":
		doc, err := s.Store.Get(ctx, "catalogue", r.ID)
		if err != nil {
			return err
		}
		return s.documentAccess(ctx, p, doc)
	case "entity", "resource":
		// Resource scopes use "*" for catalogue entries without a concrete
		// group. Every other captured resource identifies an entity, even
		// when an explicit permission survives deletion of that entity.
		if r.Kind != "resource" || r.ID != "*" {
			if _, err := s.Store.Get(ctx, "entity", r.ID); err != nil {
				return err
			}
		}
		return s.Identity.Permit(ctx, p, "read", r.ID)
	default:
		return identity.ErrDenied
	}
}

func (s *Investigations) documentAccess(ctx context.Context, p identity.Principal, doc store.Document) error {
	if doc.Kind == "definition" {
		_, err := s.ReadDefinition(ctx, p, doc.ID)
		return err
	}
	if doc.Kind == "draft" {
		_, err := s.Definitions.LoadDraft(ctx, p, DraftInput{ID: doc.ID}, "read")
		return err
	}
	var item map[string]any
	if err := store.DecodeJSON(doc.Data, &item); err != nil {
		return err
	}
	if def, ok := item["definition_id"].(string); ok && def != "" {
		_, err := s.ReadDefinition(ctx, p, def)
		return err
	}
	group, _ := item["group_id"].(string)
	if group == "" {
		group = "*"
	}
	return s.Identity.Permit(ctx, p, "read", group)
}

type aiDocumentCursor struct {
	User, Collection, Kind, Last string
	Expires                      int64
}

func (s *Investigations) Documents(ctx context.Context, p identity.Principal, collection string, scope model.AIDocumentScope) (model.AIDocumentPage, error) {
	out := model.AIDocumentPage{Items: []json.RawMessage{}, Scope: scope, ScanBudget: AIDocumentScanBudget}
	if collection != "catalogue" && collection != "definition" && collection != "draft" {
		return out, errors.New("invalid document collection")
	}
	if _, err := s.authorize(ctx, p); err != nil {
		return out, err
	}
	if scope.Limit == 0 {
		scope.Limit = 100
	}
	if scope.Limit < 1 || scope.Limit > 200 {
		return out, errors.New("limit must be between 1 and 200")
	}
	out.Scope = scope
	after := ""
	if scope.After != "" {
		raw, err := s.Identity.Decrypt("ai-page:"+p.User.ID, scope.After)
		if err != nil {
			return out, store.ErrQueryInvalid
		}
		var cursor aiDocumentCursor
		if err = store.DecodeJSON([]byte(raw), &cursor); err != nil || cursor.User != p.User.ID || cursor.Collection != collection || cursor.Kind != scope.Kind {
			return out, store.ErrQueryInvalid
		}
		if cursor.Expires <= s.Store.CurrentTime().UnixMilli() {
			return out, store.ErrQueryExpired
		}
		after = cursor.Last
	}
	candidates, err := s.Store.AIDocuments(ctx, collection, after, AIDocumentScanBudget+1)
	if err != nil {
		return out, err
	}
	last := after
	for i, doc := range candidates {
		if i == AIDocumentScanBudget {
			out.HasMore = true
			out.BudgetExhausted = true
			break
		}
		last = doc.ID
		var item map[string]any
		if err = store.DecodeJSON(doc.Data, &item); err != nil {
			return out, err
		}
		if scope.Kind != "" && item["kind"] != scope.Kind {
			continue
		}
		item, err = s.readAIDocument(ctx, p, doc)
		if err != nil {
			if errors.Is(err, identity.ErrDenied) || errors.Is(err, store.ErrNotFound) {
				continue
			}
			return out, err
		}
		// The returned storage revision belongs to the exact candidate body.
		item["version"] = doc.Version
		raw, err := json.Marshal(item)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, raw)
		if len(out.Items) == scope.Limit {
			out.HasMore = i+1 < len(candidates)
			break
		}
	}
	if out.HasMore {
		raw, _ := json.Marshal(aiDocumentCursor{User: p.User.ID, Collection: collection, Kind: scope.Kind, Last: last, Expires: s.Store.CurrentTime().Add(15 * time.Minute).UnixMilli()})
		out.NextAfter, err = s.Identity.Encrypt("ai-page:"+p.User.ID, string(raw))
	}
	return out, err
}

// Resources follows each tool's typed authorization objects. Values and node
// extension parameters remain data even when their keys resemble identities.
func (s *Investigations) Resources(name string, args map[string]any, value any) []model.EvidenceResource {
	out := []model.EvidenceResource{}
	seen := map[string]int{}
	add := func(kind, id string, version int64, revision string) {
		if id == "" {
			return
		}
		key := kind + ":" + id
		if index, exists := seen[key]; exists {
			if out[index].Version == 0 && version > 0 {
				out[index].Version = version
			}
			if out[index].Revision == "" {
				out[index].Revision = revision
			}
			return
		}
		seen[key] = len(out)
		path := ""
		switch kind {
		case "definition":
			path = "/definitions/" + url.PathEscape(id) + "/versions"
		case "draft":
			path = "/drafts/" + url.PathEscape(id) + "/diff"
		case "execution":
			path = "/executions/" + url.PathEscape(id)
		case "analysis_run":
			path = "/analysis-runs/" + url.PathEscape(id)
		case "alarm":
			path = "/alarms/" + url.PathEscape(id)
		}
		if path != "" {
			path = "/api/sf/v1" + path
		}
		out = append(out, model.EvidenceResource{Kind: kind, ID: id, Version: version, Revision: revision, URL: path})
	}
	raw, _ := json.Marshal(value)
	var root map[string]json.RawMessage
	_ = store.DecodeJSON(raw, &root)
	metadata := func(fields map[string]json.RawMessage) {
		var resources []model.EvidenceResource
		_ = store.DecodeJSON(fields["_resources"], &resources)
		for _, r := range resources {
			add(r.Kind, r.ID, r.Version, r.Revision)
		}
	}
	revision := func(field string) int64 {
		var n json.Number
		_ = json.Unmarshal(root[field], &n)
		v, _ := n.Int64()
		return v
	}
	point := func(p model.Observation) { add("entity", p.DeviceID, p.EntityRevision, "") }
	execution := func(e model.Execution) {
		add("execution", e.DownlinkID, e.Version, "")
		for _, p := range e.Snapshot {
			point(p)
		}
	}
	id, _ := args["id"].(string)
	switch name {
	case "get_definition", "explain_definition", "describe_output", "save_draft", "diff_draft", "validate_draft", "simulate_draft":
		metadata(root)
		if name == "save_draft" {
			var draft model.Draft
			_ = store.DecodeJSON(raw, &draft)
			add("draft", draft.ID, draft.Version, "")
		}
		if name == "diff_draft" || name == "validate_draft" || name == "simulate_draft" {
			add("draft", id, revision("draft_version"), "")
		}
	case "list_definitions", "discover_catalogue", "list_drafts":
		var items []map[string]json.RawMessage
		_ = store.DecodeJSON(root["items"], &items)
		for _, item := range items {
			metadata(item)
		}
	case "query_data", "list_alarms", "list_executions":
		var page model.QueryPage
		if store.DecodeJSON(raw, &page) != nil {
			break
		}
		if name == "query_data" {
			for _, device := range page.Scope.ResourceIDs {
				add("entity", device, 0, "")
			}
		}
		for _, row := range page.Items {
			switch row.Kind {
			case "trend":
				var p model.Observation
				_ = store.DecodeJSON(row.Data, &p)
				point(p)
			case "alarms":
				var a model.Alarm
				_ = store.DecodeJSON(row.Data, &a)
				add("alarm", a.ID, row.Version, row.Revision)
				add("entity", a.EntityID, 0, "")
			case "executions":
				var e model.Execution
				_ = store.DecodeJSON(row.Data, &e)
				execution(e)
			case "entities":
				add("entity", row.ID, row.Version, row.Revision)
			}
		}
	case "get_execution":
		var detail model.ExecutionDetail
		_ = store.DecodeJSON(raw, &detail)
		execution(detail.Execution)
		for _, item := range detail.Evidence {
			add("entity", item.DeviceID, 0, "")
		}
	case "get_analysis_run":
		var run historymodel.Run
		_ = store.DecodeJSON(raw, &run)
		add("analysis_run", run.ID, run.Version, "")
		for _, r := range run.Resources {
			add("resource", r, 0, "")
		}
	case "get_analysis_steps":
		add("analysis_run", id, revision("run_version"), "")
		var resources []string
		_ = store.DecodeJSON(root["resources"], &resources)
		for _, r := range resources {
			add("resource", r, 0, "")
		}
		var steps historymodel.StepList
		_ = store.DecodeJSON(raw, &steps)
		for _, step := range steps.Items {
			point(step.Point)
			for _, p := range step.FormalOutputs {
				point(p)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind == out[j].Kind {
			return out[i].ID < out[j].ID
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

func EvidenceURL(investigation, id string) string {
	return fmt.Sprintf("/api/sf/v1/investigations/%s/evidence/%s", url.PathEscape(investigation), url.PathEscape(id))
}

func EvidenceIDFromCitation(text string) string {
	matches := evidenceCitation.FindStringSubmatch(strings.TrimSpace(text))
	if len(matches) > 1 {
		return matches[1]
	}
	return ""
}
