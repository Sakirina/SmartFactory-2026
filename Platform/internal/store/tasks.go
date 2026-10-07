package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"competition2026/product/platform/internal/observability"
	"competition2026/product/platform/pkg/model"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver"
	"github.com/riverqueue/river/rivertype"
	"go.opentelemetry.io/otel/propagation"
)

// TaskArgs carries no business payload: outbox content and recompute records
// keep their original identities and are read by the worker after commit.
type TaskArgs struct {
	ID              string `json:"id"`
	KindName        string `json:"kind"`
	BusinessID      string `json:"business_id"`
	BusinessVersion int64  `json:"business_version"`
}

func (TaskArgs) Kind() string { return "smartfactory_task" }

func taskOptions(ctx context.Context, queue string, at time.Time) *river.InsertOpts {
	carrier := propagation.MapCarrier{}
	observability.Inject(ctx, carrier)
	// Keep only W3C trace context; arbitrary baggage is not persisted in jobs.
	metadata, _ := json.Marshal(map[string]any{"sf_trace": map[string]string{"traceparent": carrier.Get("traceparent"), "tracestate": carrier.Get("tracestate")}})
	return &river.InsertOpts{Queue: queue, MaxAttempts: 8, ScheduledAt: at, Metadata: metadata, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

func ProjectionKind(kind string) bool {
	switch kind {
	case "tb_entity", "tb_definition", "tb_alarm", "tb_telemetry", "tb_reconcile":
		return true
	}
	return false
}

func (t *Tx) EnqueueTask(kind, id string, version int64, outbox string, resources []string, at time.Time) (int64, error) {
	taskID := kind + ":" + id
	if kind == "recompute" {
		taskID = fmt.Sprintf("%s:%d", taskID, version)
	}
	if err := t.lock("task", taskID, false); err != nil {
		return 0, err
	}
	var old int64
	err := t.QueryRowContext(t.Ctx, "SELECT river_id FROM sf_tasks WHERE id=$1", taskID).Scan(&old)
	if err == nil {
		return old, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	queue := "projection"
	if kind == "recompute" || kind == "archive" {
		queue = kind
	}
	args := TaskArgs{ID: taskID, KindName: kind, BusinessID: id, BusinessVersion: version}
	result, err := t.Store.TaskClient.InsertTx(t.Ctx, t.Tx, args, taskOptions(t.Ctx, queue, at))
	if err != nil {
		return 0, err
	}
	resources = normalizeTaskResources(resources)
	raw, err := json.Marshal(resources)
	if err != nil {
		return 0, err
	}
	_, err = t.ExecContext(t.Ctx, "INSERT INTO sf_tasks(id,river_id,kind,business_id,business_version,outbox_id,resources,created_ms) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", taskID, result.Job.ID, kind, id, version, outbox, string(raw), t.Store.Now().UnixMilli())
	return result.Job.ID, err
}

// Fresh observations already hold their message and series locks. Their insert
// fails if any identity exists, so bulk enqueue needs no per-task round trips.
func (t *Tx) enqueueTelemetry(points []model.Observation) error {
	for start := 0; start < len(points); start += 250 {
		batch := points[start:min(start+250, len(points))]
		params := make([]river.InsertManyParams, 0, len(batch))
		for _, point := range batch {
			id := "tb:" + point.ID
			params = append(params, river.InsertManyParams{Args: TaskArgs{ID: "tb_telemetry:" + id, KindName: "tb_telemetry", BusinessID: id, BusinessVersion: point.Revision}, InsertOpts: taskOptions(t.Ctx, "projection", time.Time{})})
		}
		results, err := t.Store.TaskClient.InsertManyTx(t.Ctx, t.Tx, params)
		if err != nil {
			return err
		}
		rows := make([][]any, 0, len(results))
		for index, result := range results {
			point := batch[index]
			id := "tb:" + point.ID
			resources, _ := json.Marshal([]string{point.DeviceID})
			rows = append(rows, []any{"tb_telemetry:" + id, result.Job.ID, "tb_telemetry", id, point.Revision, id, string(resources), t.Store.Now().UnixMilli()})
		}
		if err = t.insertRows("sf_tasks", "id,river_id,kind,business_id,business_version,outbox_id,resources,created_ms", "", rows); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tx) enqueueRecompute(document Document) error {
	var job model.Job
	if err := DecodeJSON(document.Data, &job); err != nil {
		return err
	}
	if job.Kind != "recompute" || job.Status != "pending" {
		return nil
	}
	resources, err := t.recomputeResources(job)
	if err != nil {
		return err
	}
	_, err = t.EnqueueTask("recompute", job.ID, document.Version, "", resources, time.Now().Add(2*time.Second))
	return err
}

func (t *Tx) recomputeResources(job model.Job) ([]string, error) {
	if job.DefinitionID == "" {
		return RecomputeTaskResources(job, nil)
	}
	document, err := t.Read("definition", job.DefinitionID)
	if errors.Is(err, ErrNotFound) {
		return RecomputeTaskResources(job, nil)
	}
	if err != nil {
		return nil, err
	}
	return RecomputeTaskResources(job, &document)
}

func normalizeTaskResources(resources []string) []string {
	unique := map[string]bool{}
	for _, resource := range resources {
		if resource != "" {
			unique[resource] = true
		}
	}
	result := make([]string, 0, len(unique))
	for resource := range unique {
		result = append(result, resource)
	}
	if len(result) == 0 {
		result = append(result, "*")
	}
	sort.Strings(result)
	return result
}

// RecomputeTaskResources is shared by authenticated adoption of legacy jobs and
// transactional enqueue. The caller pins the supplied definition revision.
func RecomputeTaskResources(job model.Job, definition *Document) ([]string, error) {
	resources := []string{job.DeviceID}
	if job.DefinitionID != "" {
		if definition == nil {
			resources = append(resources, "*")
		} else {
			other, _, err := projectionResources("tb_definition", "", definition.Data)
			if err != nil {
				return nil, err
			}
			resources = append(resources, other...)
		}
	}
	return normalizeTaskResources(resources), nil
}

// AdoptFailedRecompute creates exactly one River identity for a pre-River
// failed job. Existing replay metadata stays intact; it never resets pending.
func (s *Store) AdoptFailedRecompute(ctx context.Context, id string, expected int64, actor model.Actor, authorize func(*Tx) error) (model.Job, error) {
	var result model.Job
	err := s.Write(ctx, func(tx *Tx) error {
		if authorize != nil {
			if err := authorize(tx); err != nil {
				return err
			}
		}
		doc, err := tx.Get("job", id)
		if err != nil {
			return err
		}
		job, err := Decode[model.Job](doc)
		if err != nil {
			return err
		}
		if doc.Version != expected || job.Kind != "recompute" || job.Status != "failed" || job.TaskID != "" {
			return ErrConflict
		}
		var mapped int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM sf_tasks WHERE kind='recompute' AND business_id=$1", id).Scan(&mapped); err != nil {
			return err
		}
		if mapped != 0 {
			return ErrConflict
		}
		resources, err := tx.recomputeResources(job)
		if err != nil {
			return err
		}
		job.Version, job.Status, job.Error = expected+1, "running", ""
		job.TaskID = fmt.Sprintf("recompute:%s:%d", job.ID, job.Version)
		if _, err := tx.Put("job", id, expected, job); err != nil {
			return err
		}
		if _, err := tx.EnqueueTask("recompute", id, job.Version, "", resources, time.Time{}); err != nil {
			return err
		}
		if err := tx.Audit(actor, "task.retry", id, job.TaskID, map[string]any{"task_id": job.TaskID, "legacy_adoption": true}); err != nil {
			return err
		}
		result = job
		return nil
	})
	return result, err
}

func projectionResources(kind, destination string, payload []byte) ([]string, int64, error) {
	switch kind {
	case "tb_reconcile":
		return []string{"*"}, 0, nil
	case "tb_definition":
		var d model.Definition
		if err := DecodeJSON(payload, &d); err != nil {
			return nil, 0, err
		}
		resources := append([]string{d.GroupID}, d.Selector.DeviceIDs...)
		if d.Selector.AssetID != "" {
			resources = append(resources, d.Selector.AssetID)
		}
		for _, c := range d.Policy.Conditions {
			resources = append(resources, c.DeviceID)
		}
		for _, step := range append(append([]model.Step{}, d.Policy.Steps...), d.Policy.Degraded...) {
			resources = append(resources, step.DeviceID)
		}
		return resources, d.Version, nil
	case "tb_alarm":
		var a model.Alarm
		if err := DecodeJSON(payload, &a); err != nil {
			return nil, 0, err
		}
		return []string{a.EntityID}, a.Version, nil
	case "tb_telemetry":
		var p model.Observation
		if err := DecodeJSON(payload, &p); err != nil {
			return nil, 0, err
		}
		return []string{p.DeviceID}, p.Revision, nil
	case "tb_entity":
		var e model.Entity
		if err := DecodeJSON(payload, &e); err != nil {
			return nil, 0, err
		}
		return []string{e.ID}, e.Version, nil
	}
	return []string{destination}, 0, nil
}

func (t *Tx) enqueueProjection(id, kind, destination string, payload []byte) error {
	if !ProjectionKind(kind) {
		return nil
	}
	resources, version, err := projectionResources(kind, destination, payload)
	if err != nil {
		return err
	}
	_, err = t.EnqueueTask(kind, id, version, id, resources, time.Time{})
	return err
}

const taskColumns = "id,river_id,kind,business_id,business_version,outbox_id,resources,created_ms,cancelled_ms"

func scanTask(row interface{ Scan(...any) error }) (model.Task, error) {
	var task model.Task
	var resources string
	err := row.Scan(&task.ID, &task.RiverID, &task.Kind, &task.BusinessID, &task.BusinessVersion, &task.OutboxID, &resources, &task.CreatedMS, &task.CancelRequestedMS)
	if errors.Is(err, sql.ErrNoRows) {
		return task, ErrNotFound
	}
	if err != nil {
		return task, err
	}
	err = DecodeJSON([]byte(resources), &task.Resources)
	task.AllowedActions = []string{}
	return task, err
}

func fillTask(task model.Task, row *rivertype.JobRow, job *Document) (model.Task, error) {
	task.State = string(row.State)
	task.Queue = row.Queue
	task.Attempt = row.Attempt
	task.MaxAttempts = row.MaxAttempts
	task.NextMS = row.ScheduledAt.UnixMilli()
	if row.AttemptedAt != nil {
		task.StartedMS = row.AttemptedAt.UnixMilli()
	}
	if row.FinalizedAt != nil {
		task.FinishedMS = row.FinalizedAt.UnixMilli()
	}
	if len(row.Errors) > 0 {
		task.Error = row.Errors[len(row.Errors)-1].Error
	}
	if task.State == "completed" {
		task.Progress = 1
	}
	revision, err := taskOperationRevision(row.Metadata)
	if err != nil {
		return task, err
	}
	// Version identifies the operation being confirmed, not each scheduler
	// observation. Active queue transitions, attempts, snoozes and next-run
	// timestamps do not change the meaning of cancelling the same work.
	task.Version = Hash([]any{"task-operation-v1", task.ID, task.RiverID, task.Kind,
		task.BusinessID, task.BusinessVersion, task.OutboxID, task.Queue, task.Resources,
		taskOperationState(task.State), task.CancelRequestedMS, revision, row.EncodedArgs,
		task.MaxAttempts})
	if task.State == "retryable" || task.State == "discarded" {
		task.Version = Hash([]any{task.Version, row.Errors})
	}
	if job != nil {
		j, err := Decode[model.Job](*job)
		if err != nil {
			return task, err
		}
		task.Superseded = j.TaskID != "" && j.TaskID != task.ID
		task.BusinessState = j.Status
		task.Phase = j.ReplayPhase
		task.ReplayID = j.ReplayID
		if (j.Status == "completed" || j.Status == "running") && j.ReplayPhase == "rollups" {
			task.BusinessState = "finalizing"
		}
		task.Progress = j.Progress
		task.CursorMS = j.CursorMS
		if j.Error != "" {
			task.Error = j.Error
		}
		// Progress commits preserve the selected run and input interval. A new
		// replay, changed phase, failure, cancellation or terminal result does
		// change the operation and therefore its version.
		j.Version, j.CursorMS, j.Progress = 0, 0, 0
		j.Status = taskBusinessOperationState(j.Status)
		task.Version = Hash([]any{task.Version, j})
	}
	return task, nil
}

func taskOperationState(state string) string {
	switch state {
	case "available", "scheduled", "pending", "running":
		return "active"
	default:
		return state
	}
}

func taskBusinessOperationState(state string) string {
	if state == "pending" || state == "running" {
		return "active"
	}
	return state
}

// Existing jobs predate this internal field and start at revision zero. River
// merges metadata written by its workers, so their snooze/output updates retain
// this revision and our updates retain River fields and the original sf_trace.
func taskOperationRevision(metadata []byte) (uint64, error) {
	var value struct {
		Revision uint64 `json:"sf_operation_revision"`
	}
	if len(metadata) == 0 {
		return 0, nil
	}
	err := json.Unmarshal(metadata, &value)
	return value.Revision, err
}

func (t *Tx) advanceTaskOperation(id int64, clearCancellation bool) error {
	row, err := t.Store.TaskClient.JobGetTx(t.Ctx, t.Tx, id)
	if err != nil {
		return err
	}
	revision, err := taskOperationRevision(row.Metadata)
	if err != nil {
		return err
	}
	if revision == ^uint64(0) {
		return errors.New("task operation revision exhausted")
	}
	if clearCancellation {
		// ChangeTask already owns the River row lock (or SQLite's write
		// transaction), so this is the current metadata, including all worker
		// updates committed before the retry. Remove the old cancellation key:
		// PostgreSQL checks key presence, so merging a JSON null would still
		// cancel the next snooze. JobUpdateFull supports replacement in both
		// official drivers; RawMessage preserves unrelated metadata values.
		var metadata map[string]json.RawMessage
		if err := json.Unmarshal(row.Metadata, &metadata); err != nil {
			return err
		}
		if metadata == nil {
			metadata = make(map[string]json.RawMessage)
		}
		delete(metadata, "cancel_attempted_at")
		metadata["sf_operation_revision"] = json.RawMessage(fmt.Sprint(revision + 1))
		encoded, err := json.Marshal(metadata)
		if err != nil {
			return err
		}
		_, err = t.Store.TaskClient.Driver().UnwrapExecutor(t.Tx).JobUpdateFull(t.Ctx, &riverdriver.JobUpdateFullParams{
			ID: id, MetadataDoUpdate: true, Metadata: encoded,
		})
		return err
	}
	metadata, err := json.Marshal(map[string]uint64{"sf_operation_revision": revision + 1})
	if err != nil {
		return err
	}
	_, err = t.Store.TaskClient.Driver().UnwrapExecutor(t.Tx).JobUpdate(t.Ctx, &riverdriver.JobUpdateParams{
		ID: id, MetadataDoMerge: true, Metadata: metadata,
	})
	return err
}

func (s *Store) Task(ctx context.Context, id string) (model.Task, error) {
	var result model.Task
	// A read transaction gives River state and the linked business version one
	// SQLite snapshot. PostgreSQL uses repeatable read for the same property.
	isolation := sql.LevelSerializable
	if s.Driver == "pgx" {
		isolation = sql.LevelRepeatableRead
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: isolation, ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	result, err = s.taskInTx(ctx, tx, id)
	return result, err
}
func (s *Store) taskInTx(ctx context.Context, tx *sql.Tx, id string) (model.Task, error) {
	task, err := scanTask(tx.QueryRowContext(ctx, "SELECT "+taskColumns+" FROM sf_tasks WHERE id=$1", id))
	if err != nil {
		return task, err
	}
	row, err := s.TaskClient.JobGetTx(ctx, tx, task.RiverID)
	if errors.Is(err, river.ErrNotFound) {
		return task, ErrNotFound
	}
	if err != nil {
		return task, err
	}
	var job *Document
	if task.Kind == "recompute" {
		d, e := scanDocument(tx.QueryRowContext(ctx, "SELECT kind,id,version,updated_ms,data FROM documents WHERE kind='job' AND id=$1", task.BusinessID))
		if e != nil && !errors.Is(e, ErrNotFound) {
			return task, e
		}
		if e == nil {
			job = &d
		}
	}
	result, err := fillTask(task, row, job)
	if err == nil && task.Kind == "analysis" {
		return s.fillAnalysisTask(ctx, tx, result)
	}
	return result, err
}
func (s *Store) TaskIDs(ctx context.Context, after string, limit int) ([]string, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT t.id FROM sf_tasks t JOIN river_job r ON r.id=t.river_id WHERE t.id>$1 ORDER BY t.id LIMIT $2", after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ChangeTask executes authorization revision checks before River row locks.
// No worker holds a River row lock while executing a business transaction.
func (s *Store) ChangeTask(ctx context.Context, id, expected, action string, actor model.Actor, authorize func(*Tx) error) (model.Task, error) {
	var result model.Task
	err := s.Write(ctx, func(tx *Tx) error {
		if authorize != nil {
			if err := authorize(tx); err != nil {
				return err
			}
		}
		link, err := scanTask(tx.QueryRowContext(ctx, "SELECT "+taskColumns+" FROM sf_tasks WHERE id=$1", id))
		if err != nil {
			return err
		}
		if link.Kind == "recompute" {
			if _, err = tx.Get("job", link.BusinessID); err != nil {
				return err
			}
		}
		if link.Kind == "analysis" {
			if _, err = tx.Get("analysis_run", link.BusinessID); err != nil {
				return err
			}
		}
		if err = tx.lock("task", id, false); err != nil {
			return err
		}
		if s.Driver == "pgx" {
			var locked int64
			if err = tx.QueryRowContext(ctx, "SELECT id FROM river_job WHERE id=$1 FOR UPDATE", link.RiverID).Scan(&locked); err != nil {
				return err
			}
		}
		current, err := s.taskInTx(ctx, tx.Tx, id)
		if err != nil {
			return err
		}
		if expected == "" || expected != current.Version {
			return ErrConflict
		}
		canCancel := current.State == "available" || current.State == "scheduled" || current.State == "retryable" || current.State == "running" || current.State == "pending"
		canRetry := current.State == "discarded" || current.State == "cancelled" || current.State == "retryable"
		if action == "cancel" && canCancel && current.CancelRequestedMS == 0 {
			if current.Kind == "analysis" {
				if err := tx.changeAnalysisTask(current.BusinessID, action); err != nil {
					return err
				}
			}
			if current.Kind == "recompute" {
				d, err := tx.Get("job", current.BusinessID)
				if err != nil {
					return err
				}
				j, err := Decode[model.Job](d)
				if err != nil {
					return err
				}
				if !current.Superseded && (j.Status != "completed" || j.ReplayPhase == "rollups") {
					j.Status = "cancelled"
					j.Version = d.Version + 1
					if _, err = tx.Put("job", j.ID, d.Version, j); err != nil {
						return err
					}
				}
			}
			if _, err = s.TaskClient.JobCancelTx(ctx, tx.Tx, current.RiverID); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "UPDATE sf_tasks SET cancelled_ms=$1 WHERE id=$2", s.Now().UnixMilli(), id); err != nil {
				return err
			}
		} else if action == "retry" && canRetry && !current.Superseded && current.BusinessState != "completed" {
			if current.Kind == "analysis" {
				if err := tx.changeAnalysisTask(current.BusinessID, action); err != nil {
					return err
				}
			}
			if current.Kind == "recompute" {
				d, err := tx.Get("job", current.BusinessID)
				if err != nil {
					return err
				}
				j, err := Decode[model.Job](d)
				if err != nil {
					return err
				}
				if j.Status == "completed" && j.ReplayPhase != "rollups" {
					return ErrConflict
				}
				j.Status = "running"
				j.Error = ""
				j.Version = d.Version + 1
				if _, err = tx.Put("job", j.ID, d.Version, j); err != nil {
					return err
				}
			}
			if _, err = s.TaskClient.JobRetryTx(ctx, tx.Tx, current.RiverID); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "UPDATE sf_tasks SET cancelled_ms=0 WHERE id=$1", id); err != nil {
				return err
			}
		} else {
			return ErrConflict
		}
		if err = tx.advanceTaskOperation(current.RiverID, action == "retry"); err != nil {
			return err
		}
		if err = tx.Audit(actor, "task."+action, current.BusinessID, id, map[string]any{"task_id": id, "river_id": current.RiverID, "expected_version": expected}); err != nil {
			return err
		}
		result, err = s.taskInTx(ctx, tx.Tx, id)
		return err
	})
	return result, err
}

func (s *Store) Delivery(ctx context.Context, id string) (Delivery, error) {
	var d Delivery
	var raw string
	err := s.DB.QueryRowContext(ctx, "SELECT id,kind,destination,payload,created_ms,attempts,next_ms,last_error FROM outbox WHERE id=$1", id).Scan(&d.ID, &d.Kind, &d.Destination, &raw, &d.CreatedMS, &d.Attempts, &d.NextMS, &d.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	d.Payload = json.RawMessage(raw)
	return d, err
}

// ReconcileTasks adopts work committed by binaries predating River. It runs
// once at startup; subsequent mutations enqueue in their original transaction.
func (s *Store) ReconcileTasks(ctx context.Context) error {
	jobs, err := s.List(ctx, "job")
	if err != nil {
		return err
	}
	for _, doc := range jobs {
		j, err := Decode[model.Job](doc)
		if err != nil {
			return err
		}
		if j.Kind != "recompute" || (j.Status != "pending" && j.Status != "running") {
			continue
		}
		if j.TaskID != "" {
			continue
		}
		if err = s.Write(ctx, func(tx *Tx) error {
			current, err := tx.Get("job", doc.ID)
			if err != nil {
				return err
			}
			if current.Version != doc.Version {
				return nil
			}
			j.TaskID = fmt.Sprintf("recompute:%s:%d", j.ID, current.Version+1)
			j.Status = "running"
			adopted, e := tx.Put("job", j.ID, current.Version, j)
			if e != nil {
				return e
			}
			resources, err := tx.recomputeResources(j)
			if err != nil {
				return err
			}
			_, err = tx.EnqueueTask("recompute", j.ID, adopted.Version, "", resources, time.Time{})
			return err
		}); err != nil {
			return err
		}
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT id,kind,destination,payload FROM outbox WHERE kind IN ('tb_entity','tb_definition','tb_alarm','tb_telemetry','tb_reconcile')")
	if err != nil {
		return err
	}
	var pending []Delivery
	for rows.Next() {
		var d Delivery
		var raw string
		if err = rows.Scan(&d.ID, &d.Kind, &d.Destination, &raw); err != nil {
			rows.Close()
			return err
		}
		d.Payload = []byte(raw)
		pending = append(pending, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, d := range pending {
		if err = s.Write(ctx, func(tx *Tx) error { return tx.enqueueProjection(d.ID, d.Kind, d.Destination, d.Payload) }); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tx) LockTaskSchedule() error { return t.lock("task_schedule", "archive", false) }
