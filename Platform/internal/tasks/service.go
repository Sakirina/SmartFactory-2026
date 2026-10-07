// Package tasks runs durable archive, replay and external projection work.
package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"competition2026/product/platform/internal/observability"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
	"github.com/riverqueue/river"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type Handlers struct {
	Recompute  func(context.Context, model.Job) error
	Projection func(context.Context, store.Delivery) error
	Archive    func(context.Context) (store.ArchiveStats, error)
	Analysis   func(context.Context, string) error
}

type Service struct {
	Store    Repository
	Handlers Handlers
	Client   *river.Client[*sql.Tx]
}

func New(s Backend, handlers Handlers) (*Service, error) {
	service := &Service{Store: s, Handlers: handlers}
	workers := river.NewWorkers()
	river.AddWorker(workers, &worker{service: service})
	queues := map[string]river.QueueConfig{"archive": {MaxWorkers: 1}, "maintenance": {MaxWorkers: 1}}
	registerHistoryWorker(workers, service, queues)
	if handlers.Recompute != nil {
		queues["recompute"] = river.QueueConfig{MaxWorkers: 1}
	}
	if handlers.Projection != nil {
		queues["projection"] = river.QueueConfig{MaxWorkers: 2}
	}
	client, err := s.NewTaskWorkerClient(&river.Config{
		Workers: workers, Queues: queues, PollOnly: true, FetchCooldown: 20 * time.Millisecond, FetchPollInterval: 100 * time.Millisecond,
		JobTimeout: 30 * time.Second, RescueStuckJobsAfter: 90 * time.Second,
		CompletedJobRetentionPeriod: 30 * 24 * time.Hour, CancelledJobRetentionPeriod: 30 * 24 * time.Hour, DiscardedJobRetentionPeriod: 30 * 24 * time.Hour,
		Logger: slog.Default(),
		PeriodicJobs: []*river.PeriodicJob{river.NewPeriodicJob(river.PeriodicInterval(5*time.Second), func() (river.JobArgs, *river.InsertOpts) {
			return store.TaskArgs{KindName: "archive_schedule"}, &river.InsertOpts{Queue: "maintenance", MaxAttempts: 8, UniqueOpts: river.UniqueOpts{ByArgs: true}}
		}, &river.PeriodicJobOpts{RunOnStart: true})},
	})
	if err != nil {
		return nil, err
	}
	service.Client = client
	return service, nil
}
func (s *Service) Start(ctx context.Context) error {
	if err := s.Store.ReconcileTasks(ctx); err != nil {
		return err
	}
	return s.Client.Start(ctx)
}
func (s *Service) Stop(ctx context.Context) error { return s.Client.StopAndCancel(ctx) }

type worker struct {
	river.WorkerDefaults[store.TaskArgs]
	service *Service
}

func (w *worker) Timeout(job *river.Job[store.TaskArgs]) time.Duration {
	switch job.Args.KindName {
	case "archive", "archive_schedule":
		return 5 * time.Second
	case "recompute":
		return 30 * time.Second
	default:
		return 15 * time.Second
	}
}
func (w *worker) NextRetry(job *river.Job[store.TaskArgs]) time.Time {
	return time.Now().Add(time.Duration(min(60, 1<<min(job.Attempt-1, 6))) * time.Second)
}
func (w *worker) Work(ctx context.Context, job *river.Job[store.TaskArgs]) (workErr error) {
	var metadata struct {
		Trace map[string]string `json:"sf_trace"`
	}
	if json.Unmarshal(job.Metadata, &metadata) == nil {
		ctx = observability.Extract(ctx, propagation.MapCarrier(metadata.Trace))
	}
	ctx, finish := observability.StartOperation(ctx, "tasks.execute", observability.Identity{MessageID: job.Args.BusinessID})
	defer func() { finish(workErr) }()
	attributes := []attribute.KeyValue{attribute.String("smartfactory.task_id", job.Args.ID), attribute.String("smartfactory.task_kind", job.Args.KindName), attribute.String("smartfactory.task_queue", job.Queue), attribute.String("smartfactory.business_id", job.Args.BusinessID), attribute.Int("smartfactory.task_attempt", job.Attempt)}
	trace.SpanFromContext(ctx).SetAttributes(attributes...)
	if job.AttemptedAt != nil {
		waiting := max(0, job.AttemptedAt.Sub(job.ScheduledAt).Seconds())
		waitCtx, waited := observability.StartOperation(ctx, "tasks.wait", observability.Identity{MessageID: job.Args.BusinessID})
		trace.SpanFromContext(waitCtx).SetAttributes(append(attributes, attribute.Float64("smartfactory.wait_seconds", waiting))...)
		if histogram, err := otel.Meter("smartfactory/tasks").Float64Histogram("smartfactory.task.wait", metric.WithUnit("s")); err == nil {
			histogram.Record(waitCtx, waiting, metric.WithAttributes(attribute.String("smartfactory.queue", job.Queue)))
		}
		waited(nil)
	}
	s := w.service
	if job.Args.KindName == "archive_schedule" {
		return s.Store.ScheduleArchive(ctx)
	}
	task, err := s.Store.Task(ctx, job.Args.ID)
	if err != nil {
		return err
	}
	if task.CancelRequestedMS > 0 {
		return river.JobCancel(errors.New("task cancelled"))
	}
	switch job.Args.KindName {
	case "archive":
		archive := s.Handlers.Archive
		if archive == nil {
			archive = s.Store.ArchiveObservations
		}
		_, err = archive(ctx)
		return err
	case "recompute":
		if s.Handlers.Recompute == nil {
			return errors.New("recompute handler unavailable")
		}
		doc, err := s.Store.Get(ctx, "job", job.Args.BusinessID)
		if err != nil {
			return err
		}
		business, err := store.Decode[model.Job](doc)
		if err != nil {
			return err
		}
		if business.TaskID != "" && business.TaskID != job.Args.ID {
			return nil
		}
		if business.Status == "cancelled" {
			return river.JobCancel(errors.New("recompute cancelled"))
		}
		if business.Status == "completed" && business.ReplayPhase != "rollups" {
			return nil
		}
		business.Version = doc.Version
		err = s.Handlers.Recompute(ctx, business)
		if err != nil && ctx.Err() == nil {
			// Preserve any concurrent cancellation or replacement by a later batch.
			saveErr := s.Store.RecordRecomputeFailure(ctx, business.TaskID, business.ID, err)
			return errors.Join(err, saveErr)
		}
		if err != nil {
			return err
		}
		current, e := s.Store.Task(ctx, job.Args.ID)
		if e != nil {
			return e
		}
		if current.CancelRequestedMS > 0 {
			return river.JobCancel(errors.New("recompute cancelled"))
		}
		return nil
	default:
		if !store.ProjectionKind(job.Args.KindName) || s.Handlers.Projection == nil {
			return fmt.Errorf("projection handler unavailable for %s", job.Args.KindName)
		}
		delivery, err := s.Store.Delivery(ctx, job.Args.BusinessID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err = s.Handlers.Projection(ctx, delivery); err != nil {
			return errors.Join(err, s.Store.DeliveryFailed(ctx, delivery.ID, err))
		}
		// A committed external delivery and River completion share one transaction.
		return s.Store.CompleteProjection(ctx, delivery.ID, job.ID)
	}
}
