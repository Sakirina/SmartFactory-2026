package application

import (
	"context"
	"errors"
	"testing"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

// This adapter changes real database state after authorization reads. The
// underlying business transaction must detect its captured revision changing.
type changedTaskRepository struct {
	TaskRepository
	before func() error
}

func (r *changedTaskRepository) ChangeTask(ctx context.Context, id, expected, action string, actor model.Actor, guard func(*store.Tx) error) (model.Task, error) {
	if err := r.before(); err != nil {
		return model.Task{}, err
	}
	return r.TaskRepository.ChangeTask(ctx, id, expected, action, actor, guard)
}
func (r *changedTaskRepository) AdoptFailedRecompute(ctx context.Context, id string, expected int64, actor model.Actor, guard func(*store.Tx) error) (model.Job, error) {
	if err := r.before(); err != nil {
		return model.Job{}, err
	}
	return r.TaskRepository.AdoptFailedRecompute(ctx, id, expected, actor, guard)
}

func TestLegacyTaskRetryPinsAuthorizationAndDefinitionRevisions(t *testing.T) {
	for _, mapped := range []bool{false, true} {
		name := "unmapped-definition"
		if mapped {
			name = "mapped-grant-membership"
		}
		t.Run(name, func(t *testing.T) {
			fixture, principal, draft := definitionsFixture(t)
			ctx := context.Background()
			definition, err := fixture.Store.Put(ctx, "definition", draft.Definition.ID, 0, draft.Definition)
			if err != nil {
				t.Fatal(err)
			}
			job := model.Job{ID: "legacy-failed", Kind: "recompute", Status: "failed", DeviceID: "device", DefinitionID: draft.Definition.ID, FromMS: 1, ToMS: 2}
			if mapped {
				job.Status = "pending"
			}
			doc, err := fixture.Store.Put(ctx, "job", job.ID, 0, job)
			if err != nil {
				t.Fatal(err)
			}
			job, _ = store.Decode[model.Job](doc)
			if mapped {
				job.Status = "failed"
				doc, err = fixture.Store.Put(ctx, "job", job.ID, doc.Version, job)
				if err != nil {
					t.Fatal(err)
				}
				task, err := fixture.Store.Task(ctx, job.TaskID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = fixture.Store.TaskClient.JobCancel(ctx, task.RiverID); err != nil {
					t.Fatal(err)
				}
			}
			repository := &changedTaskRepository{TaskRepository: fixture.Store, before: func() error {
				if mapped {
					_, err := fixture.Store.Put(ctx, "grant", "concurrent-grant", 0, map[string]any{"id": "concurrent-grant"})
					return err
				}
				changed := draft.Definition
				changed.Selector.DeviceIDs = []string{"forbidden"}
				_, err := fixture.Store.Put(ctx, "definition", definition.ID, definition.Version, changed)
				return err
			}}
			application := &Tasks{Store: repository, Identity: fixture.Identity}
			if _, err := application.RetryJob(ctx, principal, job.ID); !errors.Is(err, store.ErrConflict) {
				t.Fatal(err)
			}
			after, err := fixture.Store.Get(ctx, "job", job.ID)
			if err != nil || after.Version != doc.Version || string(after.Data) != string(doc.Data) {
				t.Fatal("authorization conflict changed the business job", after, err)
			}
			var count int
			want := 0
			if mapped {
				want = 1
			}
			if err := fixture.Store.DB.QueryRow("SELECT COUNT(*) FROM sf_tasks").Scan(&count); err != nil || count != want {
				t.Fatal(count, err)
			}
		})
	}
}
