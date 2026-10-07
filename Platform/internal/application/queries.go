package application

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"competition2026/product/platform/internal/control"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type Queries struct {
	Store        *store.Store
	Identity     *identity.Manager
	Control      *control.Service
	PollInterval time.Duration
	Buffer       int
	mu           sync.Mutex
	groups       map[string]*queryGroup
	cancel       context.CancelFunc
	polls        atomic.Int64
	pageReads    atomic.Int64
	loops        atomic.Int64
}

type queryCursor struct {
	Format   int    `json:"v"`
	Epoch    string `json:"epoch"`
	Sequence int64  `json:"seq"`
	Identity string `json:"identity"`
	Query    string `json:"query"`
	AfterID  string `json:"after_id,omitempty"`
	AfterMS  int64  `json:"after_ms,omitempty"`
}
type queryAccess struct {
	principal identity.Principal
	scope     store.QueryScope
	hash      string
}

func normalizeQuery(kind string, in model.QueryRequest) (model.QueryRequest, error) {
	switch kind {
	case "entities", "alarms", "executions", "trend":
	default:
		return in, errors.New("query kind must be entities, alarms, executions or trend")
	}
	if in.Limit == 0 {
		in.Limit = 100
	}
	maximum := 500
	if kind == "trend" {
		maximum = 2000
	}
	if in.Limit < 1 || in.Limit > maximum {
		return in, fmt.Errorf("limit must be between 1 and %d", maximum)
	}
	if len(in.ResourceIDs) > 256 || len(in.Keys) > 128 || len(in.PageToken) > 4096 {
		return in, store.ErrQueryBudget
	}
	for _, value := range append(append([]string{}, in.ResourceIDs...), in.Keys...) {
		if value == "" || len(value) > 1024 || strings.ContainsRune(value, '\x00') {
			return in, errors.New("query identifiers must contain 1 to 1024 bytes and no NUL")
		}
	}
	for _, value := range []string{in.DefinitionID, in.Status, in.EntityKind, in.Search, in.AssigneeID, in.HandlingStatus} {
		if len(value) > 1024 {
			return in, store.ErrQueryBudget
		}
	}
	in.ResourceIDs = sortedUniqueQuery(in.ResourceIDs)
	in.Keys = sortedUniqueQuery(in.Keys)
	if kind == "trend" {
		if in.FromMS <= 0 || in.ToMS < in.FromMS || in.ToMS-in.FromMS > int64(3*366*24*time.Hour/time.Millisecond) {
			return in, errors.New("trend requires an explicit from_ms and to_ms covering at most three years")
		}
		if in.Resolution == "" {
			in.Resolution = "raw"
		}
		switch in.Resolution {
		case "raw", "minute", "hour", "day":
		default:
			return in, errors.New("resolution must be raw, minute, hour or day")
		}
	} else if len(in.Keys) > 0 || in.Resolution != "" {
		return in, errors.New("keys and resolution apply to trend queries")
	}
	if kind != "entities" && (in.EntityKind != "" || in.Search != "") {
		return in, errors.New("entity_kind and search apply to entity queries")
	}
	if kind != "alarms" && (in.Active != nil || in.Acknowledged != nil || in.AssigneeID != "" || in.HandlingStatus != "") {
		return in, errors.New("alarm handling filters apply to alarm queries")
	}
	if in.FromMS < 0 || in.ToMS < 0 || (in.ToMS > 0 && in.ToMS < in.FromMS) {
		return in, errors.New("invalid query time range")
	}
	return in, nil
}
func sortedUniqueQuery(in []string) []string {
	set := map[string]bool{}
	for _, v := range in {
		set[v] = true
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
func queryHash(kind string, in model.QueryRequest) string {
	in.PageToken = ""
	return store.Hash(struct {
		Kind    string
		Request model.QueryRequest
	}{kind, in})
}
func queryContains(in []string, v string) bool {
	for _, s := range in {
		if s == v {
			return true
		}
	}
	return false
}

// access captures the current principal, delegation, session and all grants.
// The database authorization revision also covers entity ancestry changes.
func (s *Queries) access(ctx context.Context, p identity.Principal) (queryAccess, error) {
	out := queryAccess{}
	state, err := s.Store.QueryState(ctx)
	if err != nil {
		return out, err
	}
	d, err := s.Store.Get(ctx, "user", p.User.ID)
	if err != nil {
		return out, identity.ErrAuthentication
	}
	u, err := store.Decode[model.User](d)
	if err != nil || !u.Active {
		return out, identity.ErrAuthentication
	}
	u.Version = d.Version
	if p.User.AI {
		u.AI = true
		u.Roles = append([]string{}, p.User.Roles...)
	}
	p.User = u
	if p.SessionDocument != "" {
		doc, e := s.Store.Get(ctx, "session", p.SessionDocument)
		if e != nil {
			return out, identity.ErrAuthentication
		}
		v, e := store.Decode[identity.Session](doc)
		if e != nil || v.UserID != u.ID || v.ExpiresMS <= s.Store.Now().UnixMilli() || doc.Version != p.SessionVersion {
			return out, identity.ErrAuthentication
		}
	}
	permissionVersion := int64(0)
	if s.Identity.Edge {
		doc, e := s.Store.Get(ctx, "permission_bundle", "active")
		if e != nil {
			return out, identity.ErrAuthentication
		}
		permissionVersion = doc.Version
		v, e := store.Decode[identity.PermissionBundle](doc)
		if e != nil || v.NodeID != s.Store.NodeID || v.ExpiresMS <= s.Store.Now().UnixMilli() {
			return out, identity.ErrAuthentication
		}
	}
	if err = s.Identity.Permit(ctx, p, "read", ""); err != nil {
		return out, err
	}
	docs, err := s.Store.List(ctx, "grant")
	if err != nil {
		return out, err
	}
	roots := append([]string{}, u.Resources...)
	versions := map[string]int64{}
	for _, doc := range docs {
		g, e := store.Decode[identity.Grant](doc)
		if e != nil {
			return out, e
		}
		if queryContains(u.Teams, g.TeamID) && queryContains(g.Actions, "read") {
			roots = append(roots, g.Resources...)
			roots = append(roots, g.GroupID)
			versions[doc.ID] = doc.Version
		}
	}
	for _, root := range roots {
		if root == "*" {
			out.scope.All = true
		}
	}
	out.scope.Roots = sortedUniqueQuery(roots)
	out.scope.AuthRevision = state.AuthRevision
	if out.scope.All {
		out.scope.AuthRevision = -1
	}
	check, err := s.Store.QueryState(ctx)
	if err != nil {
		return out, err
	}
	if check.AuthRevision != state.AuthRevision {
		return out, store.ErrQueryAuthorization
	}
	out.hash = store.Hash(struct {
		User              model.User
		Session           string
		SessionVersion    int64
		Local             bool
		StepUp            int64
		Grants            map[string]int64
		Authorization     int64
		SessionID         string
		ActorSessionID    string
		PermissionVersion int64
	}{u, p.SessionDocument, p.SessionVersion, p.Local, p.StepUpUntilMS, versions, out.scope.AuthRevision, p.SessionID, p.Actor.SessionID, permissionVersion})
	out.principal = p
	return out, nil
}

func (s *Queries) encodeCursor(c queryCursor) string {
	c.Format = 1
	raw, _ := json.Marshal(c)
	key := sha256.Sum256(append(append([]byte{}, s.Store.SignKey...), []byte("/query-cursor/v1")...))
	mac := hmac.New(sha256.New, key[:])
	mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *Queries) decodeCursor(token, hash, who string) (queryCursor, error) {
	c := queryCursor{}
	if len(token) > 4096 {
		return c, store.ErrQueryInvalid
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return c, store.ErrQueryInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return c, store.ErrQueryInvalid
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return c, store.ErrQueryInvalid
	}
	key := sha256.Sum256(append(append([]byte{}, s.Store.SignKey...), []byte("/query-cursor/v1")...))
	mac := hmac.New(sha256.New, key[:])
	mac.Write(raw)
	if !hmac.Equal(sig, mac.Sum(nil)) || json.Unmarshal(raw, &c) != nil || c.Format != 1 {
		return c, store.ErrQueryInvalid
	}
	if c.Identity != who {
		return c, store.ErrQueryAuthorization
	}
	if c.Query != hash {
		return c, store.ErrQueryChanged
	}
	return c, nil
}

func (s *Queries) Page(ctx context.Context, p identity.Principal, kind string, in model.QueryRequest) (model.QueryPage, error) {
	opts, err := normalizeQuery(kind, in)
	if err != nil {
		return model.QueryPage{}, err
	}
	access, err := s.access(ctx, p)
	if err != nil {
		return model.QueryPage{}, err
	}
	position := store.QueryPosition{}
	if opts.PageToken != "" {
		c, e := s.decodeCursor(opts.PageToken, queryHash(kind, opts), access.hash)
		if e != nil {
			return model.QueryPage{}, e
		}
		position = store.QueryPosition{Epoch: c.Epoch, Sequence: c.Sequence, AfterID: c.AfterID, AfterMS: c.AfterMS}
	}
	page, _, err := s.pageAt(ctx, kind, opts, access, position)
	return page, err
}

func (s *Queries) pageAt(ctx context.Context, kind string, opts model.QueryRequest, access queryAccess, position store.QueryPosition) (model.QueryPage, store.QueryPosition, error) {
	s.pageReads.Add(1)
	result, err := s.Store.QueryPage(ctx, kind, opts, access.scope, position)
	if err != nil {
		return model.QueryPage{}, position, err
	}
	page := model.QueryPage{Items: []model.QueryRow{}, HasMore: result.HasMore, Scope: opts, Metadata: result.Metadata}
	page.Scope.PageToken = ""
	for _, row := range result.Rows {
		if err = s.permitRow(ctx, access.principal, row); err != nil {
			if errors.Is(err, identity.ErrDenied) || errors.Is(err, store.ErrNotFound) {
				continue
			}
			return page, result.Position, err
		}
		page.Items = append(page.Items, row)
	}
	final, err := s.access(ctx, access.principal)
	if err != nil {
		return page, result.Position, err
	}
	if final.hash != access.hash {
		return page, result.Position, store.ErrQueryAuthorization
	}
	c := queryCursor{Epoch: result.Position.Epoch, Sequence: result.Position.Sequence, Identity: access.hash, Query: queryHash(kind, opts)}
	page.SnapshotCursor = s.encodeCursor(c)
	if result.HasMore && len(result.Rows) > 0 {
		last := result.Rows[len(result.Rows)-1]
		c.AfterID = last.ID
		c.AfterMS = last.SortMS
		page.NextPageToken = s.encodeCursor(c)
	}
	return page, result.Position, nil
}

func (s *Queries) permitRow(ctx context.Context, p identity.Principal, row model.QueryRow) error {
	resource := ""
	switch row.Kind {
	case "entities":
		resource = row.ID
	case "alarms":
		var v model.Alarm
		if err := store.DecodeJSON(row.Data, &v); err != nil {
			return err
		}
		resource = v.EntityID
	case "trend":
		var v model.Observation
		if err := store.DecodeJSON(row.Data, &v); err != nil {
			return err
		}
		resource = v.DeviceID
	case "executions":
		if s.Control == nil {
			return identity.ErrDenied
		}
		_, err := s.Control.Detail(ctx, p, row.ID)
		return err
	default:
		return identity.ErrDenied
	}
	return s.Identity.Permit(ctx, p, "read", resource)
}

type QuerySubscription struct {
	Events <-chan model.QueryEvent
	events chan model.QueryEvent
	done   chan struct{}
	once   sync.Once
	close  func()
}

func (s *QuerySubscription) Close() {
	s.once.Do(func() {
		if s.close != nil {
			s.close()
		}
	})
}

type queryGroup struct {
	key, kind string
	opts      model.QueryRequest
	access    queryAccess
	page      model.QueryPage
	position  store.QueryPosition
	subs      map[*QuerySubscription]bool
}
type QuerySubscriptionStats struct {
	Subscribers int   `json:"subscribers"`
	Groups      int   `json:"groups"`
	Polls       int64 `json:"polls"`
	PageReads   int64 `json:"page_reads"`
	ActiveLoops int64 `json:"active_loops"`
}

func (s *Queries) SubscriptionStats() QuerySubscriptionStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := QuerySubscriptionStats{Groups: len(s.groups), Polls: s.polls.Load(), PageReads: s.pageReads.Load(), ActiveLoops: s.loops.Load()}
	for _, g := range s.groups {
		out.Subscribers += len(g.subs)
	}
	return out
}
func resetQueryEvent(err error) model.QueryEvent {
	retry := !errors.Is(err, identity.ErrAuthentication) && !errors.Is(err, identity.ErrDenied)
	return model.QueryEvent{Type: "reset", Reason: err.Error(), Clear: true, Retryable: retry}
}

// Subscribe shares a window only when query, user, delegation, session and
// authorization revisions match. Durable historical rows allow restart resume.
func (s *Queries) Subscribe(ctx context.Context, p identity.Principal, kind string, in model.QueryRequest, resume string) (*QuerySubscription, error) {
	opts, err := normalizeQuery(kind, in)
	if err != nil {
		return nil, err
	}
	if opts.PageToken != "" {
		return nil, errors.New("subscriptions track the first page; page_token is not accepted")
	}
	access, err := s.access(ctx, p)
	if err != nil {
		return nil, err
	}
	buffer := s.Buffer
	if buffer < 1 {
		buffer = 32
	}
	sub := &QuerySubscription{events: make(chan model.QueryEvent, buffer), done: make(chan struct{})}
	sub.Events = sub.events
	var previous model.QueryPage
	var resumeSequence int64
	if resume != "" {
		c, e := s.decodeCursor(resume, queryHash(kind, opts), access.hash)
		if e == nil && c.AfterID != "" {
			e = store.ErrQueryChanged
		}
		if e == nil {
			resumeSequence = c.Sequence
			previous, _, e = s.pageAt(ctx, kind, opts, access, store.QueryPosition{Epoch: c.Epoch, Sequence: c.Sequence})
		}
		if e != nil {
			sub.events <- resetQueryEvent(e)
			close(sub.events)
			close(sub.done)
			return sub, nil
		}
	}
	key := queryHash(kind, opts) + ":" + access.hash
	s.mu.Lock()
	if s.groups == nil {
		s.groups = map[string]*queryGroup{}
	}
	if len(s.groups) >= 128 && s.groups[key] == nil {
		s.mu.Unlock()
		return nil, store.ErrQueryBudget
	}
	g := s.groups[key]
	if g == nil {
		page, pos, e := s.pageAt(ctx, kind, opts, access, store.QueryPosition{})
		if e != nil {
			s.mu.Unlock()
			return nil, e
		}
		g = &queryGroup{key: key, kind: kind, opts: opts, access: access, page: page, position: pos, subs: map[*QuerySubscription]bool{}}
		s.groups[key] = g
	}
	// A cursor obtained from a direct page can be ahead of the shared poller.
	// Advance the group before attaching that client, retaining monotonic resume.
	if resumeSequence > g.position.Sequence || (resume != "" && kind == "trend") {
		page, pos, e := s.pageAt(ctx, kind, opts, access, store.QueryPosition{})
		if e != nil {
			s.mu.Unlock()
			return nil, e
		}
		event := queryDelta(g.page, page, pos.Sequence)
		g.page, g.position = page, pos
		for existing := range g.subs {
			s.sendQuery(g, existing, event)
		}
		s.groups[key] = g
	}
	count := 0
	for _, group := range s.groups {
		count += len(group.subs)
	}
	if count >= 256 {
		if len(g.subs) == 0 {
			delete(s.groups, key)
		}
		s.mu.Unlock()
		return nil, store.ErrQueryBudget
	}
	if resume == "" {
		page := g.page
		sub.events <- model.QueryEvent{Type: "snapshot", Cursor: page.SnapshotCursor, Page: &page, HasMore: page.HasMore}
	} else {
		event := queryDelta(previous, g.page, g.position.Sequence)
		if kind == "trend" {
			event.Type = "delta"
			metadata := g.page.Metadata
			event.Metadata = &metadata
		}
		sub.events <- event
	}
	g.subs[sub] = true
	sub.close = func() { s.mu.Lock(); defer s.mu.Unlock(); s.removeSubscription(g, sub) }
	if s.cancel == nil {
		loopCtx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		s.loops.Add(1)
		go s.runQueries(loopCtx)
	}
	s.mu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
			sub.Close()
		case <-sub.done:
		}
	}()
	return sub, nil
}
func queryDelta(previous, current model.QueryPage, sequence int64) model.QueryEvent {
	event := model.QueryEvent{Type: "delta", Cursor: current.SnapshotCursor, HasMore: current.HasMore, Metadata: &current.Metadata, Changes: []model.QueryChange{}}
	before := map[string]model.QueryRow{}
	after := map[string]bool{}
	for _, r := range previous.Items {
		before[r.ID] = r
	}
	for _, r := range current.Items {
		after[r.ID] = true
		old, ok := before[r.ID]
		if !ok || old.Revision != r.Revision {
			item := r
			event.Changes = append(event.Changes, model.QueryChange{Operation: "upsert", Kind: r.Kind, ID: r.ID, Revision: r.Revision, Item: &item})
		}
	}
	for _, r := range previous.Items {
		if !after[r.ID] {
			event.Changes = append(event.Changes, model.QueryChange{Operation: "remove", Kind: r.Kind, ID: r.ID, Revision: strconv.FormatInt(sequence, 10)})
		}
	}
	if len(event.Changes) == 0 && store.Hash(previous.Metadata) == store.Hash(current.Metadata) {
		event.Type = "checkpoint"
		event.Metadata = nil
	}
	return event
}
func (s *Queries) removeSubscription(g *queryGroup, sub *QuerySubscription) {
	if !g.subs[sub] {
		return
	}
	delete(g.subs, sub)
	close(sub.events)
	close(sub.done)
	if len(g.subs) == 0 {
		delete(s.groups, g.key)
	}
	if len(s.groups) == 0 && s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}
func (s *Queries) sendQuery(g *queryGroup, sub *QuerySubscription, event model.QueryEvent) {
	select {
	case sub.events <- event:
	default:
		draining := true
		for draining {
			select {
			case <-sub.events:
			default:
				draining = false
			}
		}
		sub.events <- resetQueryEvent(errors.New("slow_consumer"))
		s.removeSubscription(g, sub)
	}
}
func (s *Queries) runQueries(ctx context.Context) {
	defer s.loops.Add(-1)
	interval := s.PollInterval
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		s.mu.Lock()
		type work struct {
			group    *queryGroup
			page     model.QueryPage
			position store.QueryPosition
		}
		groups := make([]work, 0, len(s.groups))
		after := int64(0)
		for _, g := range s.groups {
			groups = append(groups, work{g, g.page, g.position})
			if after == 0 || g.position.Sequence < after {
				after = g.position.Sequence
			}
		}
		s.mu.Unlock()
		if len(groups) == 0 {
			return
		}
		s.polls.Add(1)
		state, err := s.Store.QueryState(ctx)
		var commits []store.QueryCommit
		if err == nil && state.Head > after {
			commits, err = s.Store.QueryCommits(ctx, after, 512)
		}
		for _, item := range groups {
			g := item.group
			if ctx.Err() != nil {
				return
			}
			current, accessErr := s.access(ctx, g.access.principal)
			if ctx.Err() != nil {
				return
			}
			failure := err
			if accessErr != nil {
				failure = accessErr
			} else if current.hash != g.access.hash {
				failure = store.ErrQueryAuthorization
			}
			if failure == nil && item.position.Epoch != state.Epoch {
				failure = store.ErrQueryReset
			}
			if failure == nil && item.position.Sequence < state.Floor {
				failure = store.ErrQueryExpired
			}
			if failure != nil {
				s.mu.Lock()
				if s.groups[g.key] == g {
					for sub := range g.subs {
						s.sendQuery(g, sub, resetQueryEvent(failure))
						s.removeSubscription(g, sub)
					}
				}
				s.mu.Unlock()
				continue
			}
			if state.Head <= item.position.Sequence {
				// Offline status follows elapsed heartbeat time even without a new
				// business commit. It updates metadata on the existing data cursor.
				if g.kind == "trend" {
					page := item.page
					page.Metadata.Sources = append([]model.SourceState{}, page.Metadata.Sources...)
					changed := false
					for i := range page.Metadata.Sources {
						source := &page.Metadata.Sources[i]
						if source.Status != "offline" && s.Store.Now().UnixMilli()-source.LastSeenMS > s.Store.Policy().OfflineMS {
							source.Status = "offline"
							source.Reason = fmt.Sprintf("heartbeat older than %d ms", s.Store.Policy().OfflineMS)
							changed = true
						}
					}
					if changed {
						s.mu.Lock()
						if s.groups[g.key] == g && g.position == item.position {
							event := queryDelta(g.page, page, g.position.Sequence)
							g.page = page
							for sub := range g.subs {
								s.sendQuery(g, sub, event)
							}
						}
						s.mu.Unlock()
					}
				}
				continue
			}
			affected := len(commits) >= 512
			for _, commit := range commits {
				if commit.Sequence <= item.position.Sequence {
					continue
				}
				for _, kind := range commit.Kinds {
					if kind == g.kind || kind == "rebuild" || kind == "authorization" || (g.kind == "trend" && (kind == "sources" || kind == "revisions" || kind == "gaps")) {
						affected = true
					}
				}
			}
			page, pos := item.page, item.position
			if affected {
				page, pos, failure = s.pageAt(ctx, g.kind, g.opts, current, store.QueryPosition{Epoch: state.Epoch, Sequence: state.Head})
			} else {
				pos.Sequence = state.Head
				page.SnapshotCursor = s.encodeCursor(queryCursor{Epoch: pos.Epoch, Sequence: pos.Sequence, Identity: current.hash, Query: queryHash(g.kind, g.opts)})
				if page.HasMore && len(page.Items) > 0 {
					last := page.Items[len(page.Items)-1]
					page.NextPageToken = s.encodeCursor(queryCursor{Epoch: pos.Epoch, Sequence: pos.Sequence, Identity: current.hash, Query: queryHash(g.kind, g.opts), AfterID: last.ID, AfterMS: last.SortMS})
				}
			}
			if ctx.Err() != nil {
				return
			}
			s.mu.Lock()
			if s.groups[g.key] == g && pos.Sequence >= g.position.Sequence {
				if failure != nil {
					for sub := range g.subs {
						s.sendQuery(g, sub, resetQueryEvent(failure))
						s.removeSubscription(g, sub)
					}
				} else {
					event := queryDelta(g.page, page, pos.Sequence)
					g.page = page
					g.position = pos
					for sub := range g.subs {
						s.sendQuery(g, sub, event)
					}
				}
			}
			s.mu.Unlock()
		}
	}
}
