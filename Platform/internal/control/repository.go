package control

import (
	"context"
	"time"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

// Repository is consumed by every control use case. Database drivers, action
// journals and transition/evidence SQL stay in the store implementation.
type Repository interface {
	Get(context.Context, string, string) (store.Document, error)
	List(context.Context, string) ([]store.Document, error)
	Write(context.Context, func(*store.Tx) error) error
	CurrentTime() time.Time
	Policy() store.RuntimePolicy
	Latest(context.Context, string, string) (model.Observation, error)
	AcquireControl(context.Context) (func(), error)
	ControlAction(context.Context, string) (store.ControlAction, error)
	ExecutionActions(context.Context, string) ([]store.ControlAction, error)
	ExecutionTransitions(context.Context, string) ([]model.ExecutionTransition, error)
	ExecutionEvidence(context.Context, string) ([]model.CommandEvidence, error)
	AuditList(context.Context, string, int) ([]store.AuditEvent, error)
}

var _ Repository = (*store.Store)(nil)
