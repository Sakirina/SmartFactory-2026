package api

import (
	"context"
	"net/http"

	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/pkg/model"
	"github.com/danielgtaylor/huma/v2"
)

type TaskRequest struct {
	ID string `path:"id" minLength:"1"`
}
type TaskListRequest struct {
	After string `query:"after"`
	Limit int    `query:"limit" default:"100" minimum:"1" maximum:"500"`
}
type TaskChangeRequest struct {
	ID   string `path:"id" minLength:"1"`
	Body model.TaskAction
}
type TaskResponse struct{ Body model.Task }
type TaskListResponse struct{ Body application.TaskList }
type QueueBudgetResponse struct{ Body []model.QueueBudget }

func TaskContractTypes() []any {
	return []any{model.Task{}, model.TaskAction{}, model.QueueBudget{}, application.TaskList{}}
}
func (s *Server) TaskApplication() *application.Tasks {
	return &application.Tasks{Store: s.Store, Identity: s.Identity}
}
func (s *Server) registerTaskRoutes(api huma.API, operation func(string, string, string) huma.Operation) {
	list := operation("get_tasks", "/tasks", "read")
	list.Method = http.MethodGet
	huma.Register(api, list, func(ctx context.Context, in *TaskListRequest) (*TaskListResponse, error) {
		p, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		result, err := s.TaskApplication().List(ctx, p, in.After, in.Limit)
		return &TaskListResponse{Body: result}, contractError(err)
	})
	detail := operation("get_tasks_id", "/tasks/{id}", "read")
	detail.Method = http.MethodGet
	huma.Register(api, detail, func(ctx context.Context, in *TaskRequest) (*TaskResponse, error) {
		p, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		result, err := s.TaskApplication().Get(ctx, p, in.ID)
		return &TaskResponse{Body: result}, contractError(err)
	})
	for _, action := range []string{"retry", "cancel"} {
		op := operation("post_tasks_id_"+action, "/tasks/{id}/"+action, "register")
		op.Description = "Requires the exact task version returned by GET; rechecks role, every resource, authorization revisions and actual River state. Cancellation preserves committed historical results and stops future work; retry reuses the durable business identity."
		huma.Register(api, op, func(ctx context.Context, in *TaskChangeRequest) (*TaskResponse, error) {
			p, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
			result, err := s.TaskApplication().Change(ctx, p, in.ID, in.Body.ExpectedVersion, action)
			return &TaskResponse{Body: result}, contractError(err)
		})
	}
	budgets := operation("get_task_queues", "/task-queues", "read")
	budgets.Method = http.MethodGet
	huma.Register(api, budgets, func(ctx context.Context, _ *struct{}) (*QueueBudgetResponse, error) {
		p, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		if err := s.Identity.Permit(ctx, p, "read", "*"); err != nil {
			return nil, contractError(err)
		}
		result, err := s.Store.QueueBudgets(ctx)
		return &QueueBudgetResponse{Body: result}, contractError(err)
	})
}
