package application

import (
	"context"
	"time"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

// DocumentQueries provides the versioned records used by resource authorization.
type DocumentQueries interface {
	Get(context.Context, string, string) (store.Document, error)
	List(context.Context, string) ([]store.Document, error)
}

// DefinitionRepository commits a prepared definition mutation as one business
// transaction. SQL execution and database driver selection belong to Store.
type DefinitionRepository interface {
	DocumentQueries
	CurrentTime() time.Time
	Write(context.Context, func(*store.Tx) error) error
}

// TaskRepository exposes durable task operations to authenticated use cases.
type TaskRepository interface {
	DocumentQueries
	Task(context.Context, string) (model.Task, error)
	TaskIDs(context.Context, string, int) ([]string, error)
	ChangeTask(context.Context, string, string, string, model.Actor, func(*store.Tx) error) (model.Task, error)
	AdoptFailedRecompute(context.Context, string, int64, model.Actor, func(*store.Tx) error) (model.Job, error)
}

var _ DefinitionRepository = (*store.Store)(nil)
var _ TaskRepository = (*store.Store)(nil)
