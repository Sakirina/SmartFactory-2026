package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"competition2026/product/platform/internal/historymodel"
	"competition2026/product/platform/internal/observability"
	"github.com/riverqueue/river"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type historyWorker struct {
	river.WorkerDefaults[historymodel.TaskArgs]
	service *Service
}

func registerHistoryWorker(workers *river.Workers, s *Service, queues map[string]river.QueueConfig) {
	if s.Handlers.Analysis != nil {
		queues["history_analysis"] = river.QueueConfig{MaxWorkers: 1}
		river.AddWorker(workers, &historyWorker{service: s})
	}
}
func (w *historyWorker) Timeout(*river.Job[historymodel.TaskArgs]) time.Duration {
	return 30 * time.Second
}
func (w *historyWorker) NextRetry(job *river.Job[historymodel.TaskArgs]) time.Time {
	return time.Now().Add(time.Duration(min(60, 1<<min(job.Attempt-1, 6))) * time.Second)
}
func (w *historyWorker) Work(ctx context.Context, job *river.Job[historymodel.TaskArgs]) (err error) {
	var metadata struct {
		Trace map[string]string `json:"sf_trace"`
	}
	if json.Unmarshal(job.Metadata, &metadata) == nil {
		ctx = observability.Extract(ctx, propagation.MapCarrier(metadata.Trace))
	}
	ctx, finish := observability.StartOperation(ctx, "analysis.execute", observability.Identity{MessageID: job.Args.RunID})
	defer func() { finish(err) }()
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("smartfactory.run_id", job.Args.RunID), attribute.String("smartfactory.task_id", job.Args.ID))
	task, err := w.service.Store.Task(ctx, job.Args.ID)
	if err != nil {
		return err
	}
	if task.CancelRequestedMS > 0 {
		return river.JobCancel(errors.New("analysis cancelled"))
	}
	err = w.service.Handlers.Analysis(ctx, job.Args.RunID)
	if errors.Is(err, historymodel.ErrParentPending) {
		return river.JobSnooze(500 * time.Millisecond)
	}
	if errors.Is(err, context.Canceled) && ctx.Err() == nil {
		return river.JobCancel(err)
	}
	return err
}
