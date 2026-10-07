package store

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"competition2026/product/platform/pkg/model"
)

const QueryHistoryCommits int64 = 4096
const QueryRebuildTimeout = 30 * time.Second

type QueryState struct {
	Head         int64  `json:"head"`
	Floor        int64  `json:"floor"`
	Epoch        string `json:"epoch"`
	AuthRevision int64  `json:"auth_revision"`
}

type queryRecord struct {
	Kind, ID                                                                                    string
	Version, SortMS                                                                             int64
	Resource, Parent, Name, Status, EntityKind, Definition, Key, Resolution, Assignee, Handling string
	Active, Acknowledged                                                                        int
	Data                                                                                        json.RawMessage
	Resources                                                                                   []string
	Deleted                                                                                     bool
}

func queryIdentity(kind, id string) string { return kind + "\x00" + id }
func queryPointIdentity(p model.Observation, resolution string) string {
	parts := []any{p.ID, p.ObservedMS}
	if p.DefinitionID != "" || resolution != "raw" {
		parts = []any{p.DeviceID, p.Key, p.ObservedMS}
	}
	raw, _ := json.Marshal(parts)
	class := "observation"
	if p.DefinitionID != "" {
		class = "derived"
	}
	return resolution + ":" + class + ":" + base64.RawURLEncoding.EncodeToString(raw)
}

func (t *Tx) markQueryDocument(kind, id string) {
	if !t.managed {
		return
	}
	switch kind {
	case "user", "grant", "permission_bundle", "department":
		t.queryAuth = true
	case "entity":
	case "alarm", "alarm_case", "execution", "tb_mapping", "revision", "source", "definition", "work_order", "handover", "template_batch", "alarm_operation", "native_alarm_state":
	default:
		return
	}
	if t.queryDocuments == nil {
		t.queryDocuments = map[string][2]string{}
	}
	t.queryDocuments[queryIdentity(kind, id)] = [2]string{kind, id}
}

func queryPointRecord(p model.Observation, resolution string) (queryRecord, error) {
	raw, err := json.Marshal(p)
	return queryRecord{Kind: "trend", ID: queryPointIdentity(p, resolution), Version: p.Revision, SortMS: p.ObservedMS, Resource: p.DeviceID, Definition: p.DefinitionID, Key: p.Key, Resolution: resolution, Data: raw, Resources: []string{p.DeviceID}}, err
}
func (t *Tx) markQueryPoint(p model.Observation, resolution string) error {
	if !t.managed {
		return nil
	}
	r, err := queryPointRecord(p, resolution)
	if err != nil {
		return err
	}
	if t.queryPoints == nil {
		t.queryPoints = map[string]queryRecord{}
	}
	k := queryIdentity(r.Kind, r.ID)
	if previous, ok := t.queryPoints[k]; ok && olderDerived(r, previous) {
		return nil
	}
	t.queryPoints[k] = r
	return nil
}
func (t *Tx) markQueryGap(g model.DataGap) error {
	if !t.managed {
		return nil
	}
	b, err := json.Marshal(g)
	if err != nil {
		return err
	}
	if t.queryPoints == nil {
		t.queryPoints = map[string]queryRecord{}
	}
	r := queryRecord{Kind: "gaps", ID: g.ID, Version: g.ToMS, Resource: g.DeviceID, Key: g.Key, SortMS: g.FromMS, Data: b, Resources: []string{g.DeviceID}}
	t.queryPoints[queryIdentity(r.Kind, r.ID)] = r
	return nil
}
func (t *Tx) removeQueryRow(kind, id string) {
	if !t.managed {
		return
	}
	if t.queryRemoved == nil {
		t.queryRemoved = map[string][2]string{}
	}
	t.queryRemoved[queryIdentity(kind, id)] = [2]string{kind, id}
}
func (t *Tx) hasQueryChanges() bool {
	return t.queryRebuild || t.queryAuth || len(t.queryDocuments) > 0 || len(t.queryPoints) > 0 || len(t.queryRemoved) > 0
}

var queryZlibWriters = sync.Pool{New: func() any { w, _ := zlib.NewWriterLevel(io.Discard, zlib.BestSpeed); return w }}

func packQueryData(raw []byte) ([]byte, error) {
	var b bytes.Buffer
	w := queryZlibWriters.Get().(*zlib.Writer)
	defer queryZlibWriters.Put(w)
	w.Reset(&b)
	if _, err := w.Write(raw); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
func unpackQueryData(raw []byte) (json.RawMessage, error) {
	r, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	b, err := io.ReadAll(io.LimitReader(r, 4<<20+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 4<<20 {
		return nil, errors.New("query projection exceeds payload budget")
	}
	return b, nil
}
func olderDerived(next, previous queryRecord) bool {
	if next.Kind != "trend" || next.Definition == "" {
		return false
	}
	var n, p model.Observation
	if DecodeJSON(next.Data, &n) != nil || DecodeJSON(previous.Data, &p) != nil {
		return false
	}
	return n.Revision < p.Revision || (n.Revision == p.Revision && (n.ReceivedMS < p.ReceivedMS || (n.ReceivedMS == p.ReceivedMS && n.ID < p.ID)))
}
func (t *Tx) currentQueryRecord(kind, id string) (queryRecord, error) {
	r := queryRecord{Kind: kind, ID: id}
	var raw []byte
	var deleted int
	err := t.QueryRowContext(t.Ctx, "SELECT version,sort_ms,resource_id,parent_id,name,status,entity_kind,definition_id,metric_key,resolution,assignee_id,handling_status,active,acknowledged,deleted,data FROM sf_query_rows WHERE kind=$1 AND id=$2 AND valid_to=0", kind, id).Scan(&r.Version, &r.SortMS, &r.Resource, &r.Parent, &r.Name, &r.Status, &r.EntityKind, &r.Definition, &r.Key, &r.Resolution, &r.Assignee, &r.Handling, &r.Active, &r.Acknowledged, &deleted, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	r.Deleted = deleted != 0
	r.Data, err = unpackQueryData(raw)
	if err != nil {
		return r, err
	}
	rows, err := t.QueryContext(t.Ctx, "SELECT x.resource_id FROM sf_query_resources x JOIN sf_query_rows r ON r.kind=x.kind AND r.id=x.id AND r.valid_from=x.valid_from WHERE r.kind=$1 AND r.id=$2 AND r.valid_to=0 ORDER BY x.resource_id", kind, id)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return r, err
		}
		r.Resources = append(r.Resources, id)
	}
	return r, rows.Err()
}

func (t *Tx) writeQueryRecord(r queryRecord, seq int64) (bool, error) {
	old, err := t.currentQueryRecord(r.Kind, r.ID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return false, err
	}
	if err == nil && !r.Deleted && !old.Deleted && olderDerived(r, old) {
		return false, nil
	}
	if err == nil && r.Deleted && old.Deleted {
		return false, nil
	}
	if err == nil && !r.Deleted && !old.Deleted && bytes.Equal(r.Data, old.Data) && Hash(uniqueQueryStrings(r.Resources)) == Hash(old.Resources) {
		return false, nil
	}
	if errors.Is(err, ErrNotFound) && r.Deleted {
		return false, nil
	}
	if r.Deleted {
		r = old
		r.Deleted = true
		r.Data = json.RawMessage(`null`)
	}
	data, err := packQueryData(r.Data)
	if err != nil {
		return false, err
	}
	if _, err = t.ExecContext(t.Ctx, "UPDATE sf_query_rows SET valid_to=$1 WHERE kind=$2 AND id=$3 AND valid_to=0 AND valid_from<>$1", seq, r.Kind, r.ID); err != nil {
		return false, err
	}
	deleted := 0
	if r.Deleted {
		deleted = 1
	}
	_, err = t.ExecContext(t.Ctx, `INSERT INTO sf_query_rows(kind,id,valid_from,valid_to,deleted,version,sort_ms,resource_id,parent_id,name,status,entity_kind,definition_id,metric_key,resolution,assignee_id,handling_status,active,acknowledged,data) VALUES($1,$2,$3,0,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19) ON CONFLICT(kind,id,valid_from) DO UPDATE SET deleted=excluded.deleted,version=excluded.version,sort_ms=excluded.sort_ms,resource_id=excluded.resource_id,parent_id=excluded.parent_id,name=excluded.name,status=excluded.status,entity_kind=excluded.entity_kind,definition_id=excluded.definition_id,metric_key=excluded.metric_key,resolution=excluded.resolution,assignee_id=excluded.assignee_id,handling_status=excluded.handling_status,active=excluded.active,acknowledged=excluded.acknowledged,data=excluded.data`, r.Kind, r.ID, seq, deleted, r.Version, r.SortMS, r.Resource, r.Parent, r.Name, r.Status, r.EntityKind, r.Definition, r.Key, r.Resolution, r.Assignee, r.Handling, r.Active, r.Acknowledged, data)
	if err != nil {
		return false, err
	}
	if _, err = t.ExecContext(t.Ctx, "DELETE FROM sf_query_resources WHERE kind=$1 AND id=$2 AND valid_from=$3", r.Kind, r.ID, seq); err != nil {
		return false, err
	}
	for _, resource := range uniqueQueryStrings(r.Resources) {
		if _, err = t.ExecContext(t.Ctx, "INSERT INTO sf_query_resources(kind,id,valid_from,resource_id) VALUES($1,$2,$3,$4)", r.Kind, r.ID, seq, resource); err != nil {
			return false, err
		}
	}
	return true, nil
}
func uniqueQueryStrings(in []string) []string {
	set := map[string]bool{}
	for _, v := range in {
		if v != "" {
			set[v] = true
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
func (t *Tx) queryDocument(kind, id string) (Document, error) {
	return scanDocument(t.QueryRowContext(t.Ctx, "SELECT kind,id,version,updated_ms,data FROM documents WHERE kind=$1 AND id=$2", kind, id))
}

func (t *Tx) projectDocument(kind, id string) (*queryRecord, error) {
	if kind == "alarm_case" || kind == "native_alarm_state" {
		kind = "alarm"
	}
	if kind == "tb_mapping" {
		kind = "entity"
	}
	qkind := map[string]string{"entity": "entities", "alarm": "alarms", "execution": "executions", "revision": "revisions", "source": "sources"}[kind]
	if qkind == "" {
		return nil, nil
	}
	doc, err := t.queryDocument(kind, id)
	if errors.Is(err, ErrNotFound) {
		return &queryRecord{Kind: qkind, ID: id, Deleted: true}, nil
	}
	if err != nil {
		return nil, err
	}
	r := queryRecord{Kind: qkind, ID: id, Version: doc.Version, SortMS: doc.UpdatedMS, Data: doc.Data}
	switch kind {
	case "entity":
		var v model.Entity
		if err = DecodeJSON(doc.Data, &v); err != nil {
			return nil, err
		}
		v.Version = doc.Version
		mapping, e := t.queryDocument("tb_mapping", id)
		if e == nil {
			var m struct {
				Native struct {
					ID string `json:"id"`
				} `json:"native"`
			}
			if e = DecodeJSON(mapping.Data, &m); e != nil {
				return nil, e
			}
			v.TBID = m.Native.ID
		} else if !errors.Is(e, ErrNotFound) {
			return nil, e
		}
		r.Data, err = json.Marshal(v)
		r.Resource = id
		r.Parent = v.ParentID
		r.Name = strings.ToLower(v.Name)
		r.Status = v.Status
		r.EntityKind = v.Kind
		r.Resources = []string{id}
	case "alarm":
		var v model.Alarm
		if err = DecodeJSON(doc.Data, &v); err != nil {
			return nil, err
		}
		var data map[string]any
		if err = DecodeJSON(doc.Data, &data); err != nil {
			return nil, err
		}
		r.Resource = v.EntityID
		r.Definition = v.DefinitionID
		r.SortMS = v.UpdatedMS
		r.Status = v.Severity
		r.Resources = []string{v.EntityID}
		r.Handling = "open"
		data["case_version"] = int64(0)
		data["handling_status"] = "open"
		data["assignee_id"] = ""
		data["handling_updated_ms"] = int64(0)
		c, e := t.queryDocument("alarm_case", id)
		if e == nil {
			var value struct {
				Status       string `json:"status"`
				AssigneeID   string `json:"assignee_id"`
				Acknowledged bool   `json:"acknowledged"`
				UpdatedMS    int64  `json:"updated_ms"`
			}
			if e = DecodeJSON(c.Data, &value); e != nil {
				return nil, e
			}
			r.Assignee = value.AssigneeID
			r.Handling = value.Status
			v.Acknowledged = v.Acknowledged || value.Acknowledged
			data["case_version"] = c.Version
			data["handling_status"] = value.Status
			data["assignee_id"] = value.AssigneeID
			data["handling_updated_ms"] = value.UpdatedMS
			data["handling_case"] = json.RawMessage(c.Data)
		} else if !errors.Is(e, ErrNotFound) {
			return nil, e
		}
		native, e := t.queryDocument("native_alarm_state", id)
		if e == nil {
			data["native"] = json.RawMessage(native.Data)
		} else if !errors.Is(e, ErrNotFound) {
			return nil, e
		}
		if v.Active {
			r.Active = 1
		}
		if v.Acknowledged {
			r.Acknowledged = 1
		}
		data["acknowledged"] = v.Acknowledged
		r.Data, err = json.Marshal(data)
	case "execution":
		var v model.Execution
		if err = DecodeJSON(doc.Data, &v); err != nil {
			return nil, err
		}
		r.Definition = v.DefinitionID
		r.Status = v.Status
		r.SortMS = v.CreatedMS
		resources, e := t.queryExecutionResources(v)
		if e != nil {
			return nil, e
		}
		r.Resources = resources
		if len(resources) > 0 {
			r.Resource = resources[0]
		}
		var count int64
		if e = t.QueryRowContext(t.Ctx, "SELECT COUNT(*) FROM control_evidence WHERE execution_id=$1", id).Scan(&count); e != nil {
			return nil, e
		}
		var data map[string]any
		if e = DecodeJSON(doc.Data, &data); e != nil {
			return nil, e
		}
		data["evidence_count"] = count
		r.Data, err = json.Marshal(data)
	case "revision":
		var v model.Revision
		if err = DecodeJSON(doc.Data, &v); err != nil {
			return nil, err
		}
		r.Resource = v.DeviceID
		r.Key = v.Key
		r.SortMS = v.AtMS
		r.Resources = []string{v.DeviceID}
	case "source":
		r.Resource = id
		r.Resources = []string{id}
	}
	return &r, err
}

func (t *Tx) queryExecutionResources(v model.Execution) ([]string, error) {
	resources := []string{}
	current, err := t.queryDocument("definition", v.DefinitionID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if err == nil {
		var latest model.Definition
		if err = DecodeJSON(current.Data, &latest); err != nil {
			return nil, err
		}
		resources = append(resources, latest.GroupID)
	}
	d := current
	if v.DefinitionVersion > 0 {
		d, err = scanDocument(t.QueryRowContext(t.Ctx, "SELECT kind,id,version,updated_ms,data FROM document_versions WHERE kind='definition' AND id=$1 AND version=$2", v.DefinitionID, v.DefinitionVersion))
	}
	if errors.Is(err, ErrNotFound) {
		resources = append(resources, v.DefinitionID)
	} else if err != nil {
		return nil, err
	} else {
		var def model.Definition
		if err = DecodeJSON(d.Data, &def); err != nil {
			return nil, err
		}
		resources = append(resources, def.GroupID, def.Selector.AssetID)
		resources = append(resources, def.Selector.DeviceIDs...)
		for _, c := range def.Policy.Conditions {
			resources = append(resources, c.DeviceID)
		}
		for _, steps := range [][]model.Step{def.Policy.Steps, def.Policy.Degraded} {
			for _, step := range steps {
				resources = append(resources, step.DeviceID)
			}
		}
	}
	for _, p := range v.Snapshot {
		resources = append(resources, p.DeviceID)
	}
	// Projection finalization already holds the commit-order lock. Read its
	// transaction snapshot without acquiring a business evidence lock.
	evidence, err := executionEvidence(t.Ctx, t.Tx, v.DownlinkID)
	if err != nil {
		return nil, err
	}
	for _, e := range evidence {
		resources = append(resources, e.DeviceID)
	}
	return uniqueQueryStrings(resources), nil
}

func (t *Tx) finalizeQueries() error {
	if !t.hasQueryChanges() {
		return nil
	}
	if t.queryRebuild {
		return t.rebuildQueryProjection()
	}
	var head int64
	if err := t.QueryRowContext(t.Ctx, "SELECT head FROM sf_query_state WHERE singleton=1").Scan(&head); err != nil {
		return err
	}
	head++
	kinds := map[string]bool{}
	records := map[string]queryRecord{}
	// Historical execution views also authorize the definition's current group.
	// Update the affected candidate resources in this same definition transaction.
	definitionIDs := []string{}
	for _, key := range t.queryDocuments {
		if key[0] == "definition" {
			definitionIDs = append(definitionIDs, key[1])
		}
	}
	for _, id := range definitionIDs {
		rows, err := t.QueryContext(t.Ctx, "SELECT id FROM sf_query_rows WHERE kind='executions' AND definition_id=$1 AND valid_to=0 AND deleted=0", id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var executionID string
			if err = rows.Scan(&executionID); err != nil {
				rows.Close()
				return err
			}
			t.markQueryDocument("execution", executionID)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	for _, key := range t.queryDocuments {
		if key[0] == "definition" {
			// A dependency's current resource scope can change independently of
			// the execution's own state. Re-authorize cached windows on that change.
			t.queryAuth = true
		}
		r, err := t.projectDocument(key[0], key[1])
		if err != nil {
			return err
		}
		if r != nil && r.Kind == "entities" {
			old, readErr := t.currentQueryRecord(r.Kind, r.ID)
			if readErr != nil && !errors.Is(readErr, ErrNotFound) {
				return readErr
			}
			if readErr == nil && (r.Deleted != old.Deleted || r.Parent != old.Parent) {
				t.queryAuth = true
			}
		}
		if r != nil {
			records[queryIdentity(r.Kind, r.ID)] = *r
		} else {
			kinds[key[0]] = true
		}
	}
	for k, r := range t.queryPoints {
		records[k] = r
	}
	for k, r := range t.queryRemoved {
		records[k] = queryRecord{Kind: r[0], ID: r[1], Deleted: true}
	}
	keys := make([]string, 0, len(records))
	for k := range records {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		r := records[key]
		changed, err := t.writeQueryRecord(r, head)
		if err != nil {
			return err
		}
		if changed {
			kinds[r.Kind] = true
		}
	}
	if t.queryAuth {
		kinds["authorization"] = true
	}
	if len(kinds) == 0 {
		return nil
	}
	list := make([]string, 0, len(kinds))
	for k := range kinds {
		list = append(list, k)
	}
	sort.Strings(list)
	raw, _ := json.Marshal(list)
	if _, err := t.ExecContext(t.Ctx, "INSERT INTO sf_query_commits(sequence,at_ms,kinds) VALUES($1,$2,$3)", head, t.Store.Now().UnixMilli(), string(raw)); err != nil {
		return err
	}
	auth := int64(0)
	if t.queryAuth {
		auth = head
	}
	_, err := t.ExecContext(t.Ctx, "UPDATE sf_query_state SET head=$1,auth_revision=CASE WHEN $2>0 THEN $2 ELSE auth_revision END WHERE singleton=1", head, auth)
	return err
}

// RebuildQueryProjection atomically switches generations while holding the
// existing commit-finalization lock. Business writes may prepare concurrently;
// their finalization follows the new generation after this transaction commits.
func (s *Store) RebuildQueryProjection(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, QueryRebuildTimeout)
	defer cancel()
	return s.Write(ctx, func(t *Tx) error { t.queryRebuild = true; return nil })
}
func (t *Tx) rebuildQueryProjection() error {
	var head int64
	if err := t.QueryRowContext(t.Ctx, "SELECT head FROM sf_query_state WHERE singleton=1").Scan(&head); err != nil {
		return err
	}
	head++
	for _, table := range []string{"sf_query_resources", "sf_query_rows", "sf_query_commits"} {
		if _, err := t.ExecContext(t.Ctx, "DELETE FROM "+table); err != nil {
			return err
		}
	}
	for _, kind := range []string{"entity", "alarm", "execution", "revision", "source"} {
		last := ""
		for {
			rows, err := t.QueryContext(t.Ctx, "SELECT id FROM documents WHERE kind=$1 AND id>$2 ORDER BY id LIMIT 256", kind, last)
			if err != nil {
				return err
			}
			ids := []string{}
			for rows.Next() {
				var id string
				if err = rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				ids = append(ids, id)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			for _, id := range ids {
				r, err := t.projectDocument(kind, id)
				if err != nil {
					return err
				}
				if _, err = t.writeQueryRecord(*r, head); err != nil {
					return err
				}
			}
			if len(ids) < 256 {
				break
			}
			last = ids[len(ids)-1]
		}
	}
	lastMS, lastID, firstPage := int64(0), "", true
	for {
		rows, err := t.QueryContext(t.Ctx, "SELECT data FROM observations WHERE $1 OR observed_ms>$2 OR (observed_ms=$2 AND id>$3) ORDER BY observed_ms,id LIMIT 256", firstPage, lastMS, lastID)
		if err != nil {
			return err
		}
		points := []model.Observation{}
		for rows.Next() {
			var raw string
			var p model.Observation
			if err = rows.Scan(&raw); err == nil {
				err = DecodeJSON([]byte(raw), &p)
			}
			if err != nil {
				rows.Close()
				return err
			}
			points = append(points, p)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, p := range points {
			r, e := queryPointRecord(p, "raw")
			if e != nil {
				return e
			}
			if _, e = t.writeQueryRecord(r, head); e != nil {
				return e
			}
		}
		if len(points) < 256 {
			break
		}
		lastPoint := points[len(points)-1]
		lastMS, lastID, firstPage = lastPoint.ObservedMS, lastPoint.ID, false
	}
	last := ""
	for {
		rows, err := t.QueryContext(t.Ctx, "SELECT "+archiveColumns+" FROM observation_archives WHERE id>$1 ORDER BY id LIMIT 16", last)
		if err != nil {
			return err
		}
		blocks := []archiveBlock{}
		for rows.Next() {
			b, e := scanArchive(rows)
			if e != nil {
				rows.Close()
				return e
			}
			blocks = append(blocks, b)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, b := range blocks {
			points, e := decodeArchive(b)
			if e != nil {
				return e
			}
			for _, p := range points {
				r, e := queryPointRecord(p, "raw")
				if e != nil {
					return e
				}
				if _, e = t.writeQueryRecord(r, head); e != nil {
					return e
				}
			}
		}
		if len(blocks) < 16 {
			break
		}
		last = blocks[len(blocks)-1].ID
	}
	lastDevice, lastKey, lastResolution, lastBucket, firstRollup := "", "", "", int64(0), true
	for {
		rows, err := t.QueryContext(t.Ctx, "SELECT device_id,key,granularity,bucket_ms,data FROM rollups WHERE $1 OR (device_id,key,granularity,bucket_ms)>($2,$3,$4,$5) ORDER BY device_id,key,granularity,bucket_ms LIMIT 256", firstRollup, lastDevice, lastKey, lastResolution, lastBucket)
		if err != nil {
			return err
		}
		records := []queryRecord{}
		for rows.Next() {
			var device, key, res, raw string
			var at int64
			if err = rows.Scan(&device, &key, &res, &at, &raw); err != nil {
				rows.Close()
				return err
			}
			r, e := queryRollupRecord(device, key, res, at, raw)
			if e != nil {
				rows.Close()
				return e
			}
			records = append(records, r)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, r := range records {
			if _, err = t.writeQueryRecord(r, head); err != nil {
				return err
			}
		}
		if len(records) < 256 {
			break
		}
		lastRecord := records[len(records)-1]
		lastDevice, lastKey, lastResolution, lastBucket, firstRollup = lastRecord.Resource, lastRecord.Key, lastRecord.Resolution, lastRecord.SortMS, false
	}
	rows, err := t.QueryContext(t.Ctx, "SELECT data FROM data_gaps ORDER BY id")
	if err != nil {
		return err
	}
	gaps := []model.DataGap{}
	for rows.Next() {
		var raw string
		var g model.DataGap
		if err = rows.Scan(&raw); err == nil {
			err = DecodeJSON([]byte(raw), &g)
		}
		if err != nil {
			rows.Close()
			return err
		}
		gaps = append(gaps, g)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, g := range gaps {
		raw, _ := json.Marshal(g)
		r := queryRecord{Kind: "gaps", ID: g.ID, Version: g.ToMS, Resource: g.DeviceID, Key: g.Key, SortMS: g.FromMS, Data: raw, Resources: []string{g.DeviceID}}
		if _, err = t.writeQueryRecord(r, head); err != nil {
			return err
		}
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	if _, err = t.ExecContext(t.Ctx, "UPDATE sf_query_state SET head=$1,floor=$1,epoch=$2,auth_revision=$1 WHERE singleton=1", head, hex.EncodeToString(random[:])); err != nil {
		return err
	}
	_, err = t.ExecContext(t.Ctx, "INSERT INTO sf_query_commits(sequence,at_ms,kinds) VALUES($1,$2,$3)", head, t.Store.Now().UnixMilli(), `["rebuild"]`)
	return err
}

func queryRollupRecord(device, key, res string, at int64, raw string) (queryRecord, error) {
	var v struct {
		Aggregate Aggregate `json:"aggregate"`
		Unit      string    `json:"unit"`
	}
	if err := DecodeJSON([]byte(raw), &v); err != nil {
		return queryRecord{}, err
	}
	p := model.Observation{ID: fmt.Sprintf("%s:%s:%s:%d", res, device, key, at), DeviceID: device, Key: key, ObservedMS: at, TimeSource: "aggregate", Quality: "GOOD", Unit: v.Unit, Value: v.Aggregate}
	if v.Aggregate.Count == 0 {
		p.Quality = "UNCERTAIN"
	}
	return queryPointRecord(p, res)
}
func (t *Tx) markQueryRollup(device, key, res string, at int64, raw string) error {
	if !t.managed {
		return nil
	}
	r, err := queryRollupRecord(device, key, res, at, raw)
	if err != nil {
		return err
	}
	if t.queryPoints == nil {
		t.queryPoints = map[string]queryRecord{}
	}
	t.queryPoints[queryIdentity(r.Kind, r.ID)] = r
	return nil
}

func (t *Tx) expireQueryRows(resolution string, cut int64) error {
	rows, err := t.QueryContext(t.Ctx, "SELECT id FROM sf_query_rows WHERE kind='trend' AND valid_to=0 AND deleted=0 AND resolution=$1 AND sort_ms<$2", resolution, cut)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		t.removeQueryRow("trend", id)
	}
	err = rows.Err()
	rows.Close()
	return err
}

func queryState(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (QueryState, error) {
	var state QueryState
	err := db.QueryRowContext(ctx, "SELECT head,floor,epoch,auth_revision FROM sf_query_state WHERE singleton=1").Scan(&state.Head, &state.Floor, &state.Epoch, &state.AuthRevision)
	return state, err
}
func (s *Store) QueryState(ctx context.Context) (QueryState, error) { return queryState(ctx, s.DB) }

// CompactQueryHistory retains each row's value at the floor plus all subsequent
// revisions, so an unexpired snapshot continues to paginate after updates.
func (s *Store) CompactQueryHistory(ctx context.Context, retain int64) error {
	if retain < 1 {
		return errors.New("query history retention must be positive")
	}
	return s.Write(ctx, func(t *Tx) error {
		if s.Driver == "pgx" {
			if _, err := t.ExecContext(ctx, "SELECT pg_advisory_xact_lock(872190006)"); err != nil {
				return err
			}
		}
		state, err := queryState(ctx, t.Tx)
		if err != nil {
			return err
		}
		floor := max(state.Floor, state.Head-retain)
		if floor == state.Floor {
			return nil
		}
		if _, err = t.ExecContext(ctx, "DELETE FROM sf_query_resources WHERE EXISTS(SELECT 1 FROM sf_query_rows r WHERE r.kind=sf_query_resources.kind AND r.id=sf_query_resources.id AND r.valid_from=sf_query_resources.valid_from AND ((r.valid_to>0 AND r.valid_to<=$1) OR (r.valid_to=0 AND r.deleted=1 AND r.valid_from<=$1)))", floor); err != nil {
			return err
		}
		if _, err = t.ExecContext(ctx, "DELETE FROM sf_query_rows WHERE (valid_to>0 AND valid_to<=$1) OR (valid_to=0 AND deleted=1 AND valid_from<=$1)", floor); err != nil {
			return err
		}
		if _, err = t.ExecContext(ctx, "DELETE FROM sf_query_commits WHERE sequence<=$1", floor); err != nil {
			return err
		}
		_, err = t.ExecContext(ctx, "UPDATE sf_query_state SET floor=$1 WHERE singleton=1", floor)
		return err
	})
}

type QueryCommit struct {
	Sequence int64
	Kinds    []string
}

func (s *Store) QueryCommits(ctx context.Context, after int64, limit int) ([]QueryCommit, error) {
	if limit < 1 || limit > 512 {
		limit = 512
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT sequence,kinds FROM sf_query_commits WHERE sequence>$1 ORDER BY sequence LIMIT $2", after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []QueryCommit{}
	for rows.Next() {
		var c QueryCommit
		var raw string
		if err = rows.Scan(&c.Sequence, &raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &c.Kinds); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func decimalQueryRevision(n int64) string { return strconv.FormatInt(n, 10) }
