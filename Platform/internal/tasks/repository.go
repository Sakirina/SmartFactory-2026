package tasks

import (
	"context"
	"database/sql"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
	"github.com/riverqueue/river"
)

// Repository names the durable business operations performed by workers.
// Workers do not access SQL handles, transactions, or database-driver settings.
type Repository interface {
	Get(context.Context, string, string) (store.Document, error)
	Task(context.Context, string) (model.Task, error)
	ReconcileTasks(context.Context) error
	ArchiveObservations(context.Context) (store.ArchiveStats, error)
	ScheduleArchive(context.Context) error
	RecordRecomputeFailure(context.Context, string, string, error) error
	Delivery(context.Context, string) (store.Delivery, error)
	DeliveryFailed(context.Context, string, error) error
	CompleteProjection(context.Context, string, int64) error
}

// Backend adds the composition-time River client factory to domain operations.
// The concrete adapter owns driver choice; Service retains only Repository.
type Backend interface {
	Repository
	NewTaskWorkerClient(*river.Config) (*river.Client[*sql.Tx], error)
}

var _ Backend = (*store.Store)(nil)
