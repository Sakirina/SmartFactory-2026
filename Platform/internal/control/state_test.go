package control

import (
	"context"
	"errors"
	"testing"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func TestReceiptAndCheckpointPreserveConfirmedEvidenceIdentity(t *testing.T) {
	for _, replacement := range []string{"", "another-evidence"} {
		t.Run("replacement="+replacement, func(t *testing.T) {
			s, _, _ := fixture(t)
			ctx := context.Background()
			original := model.Execution{DownlinkID: "evidence-progress", DefinitionID: "plan", DefinitionVersion: 1, Status: "running", Version: 1, Fence: 2, Steps: []model.StepResult{{StepID: "first", CommandID: "evidence-progress:first", Status: "SUCCESS", EvidenceID: "original-evidence"}}}
			if _, err := s.Identity.Store.Put(ctx, "execution", original.DownlinkID, 0, original); err != nil {
				t.Fatal(err)
			}
			next := original
			next.Status, next.Version = "completed", 2
			next.Steps = append([]model.StepResult{}, original.Steps...)
			next.Steps[0].EvidenceID = replacement
			if CheckpointProgress(original, next) {
				t.Fatal("checkpoint erased or replaced confirmed evidence")
			}
			if err := s.Store.Write(ctx, func(tx *store.Tx) error { return ApplyTransition(tx, &next, 1, "receipt", TransitionOptions{}) }); !errors.Is(err, ErrTransition) {
				t.Fatal(err)
			}
			current, _ := s.Get(ctx, original.DownlinkID)
			if store.Hash(current) != store.Hash(original) {
				t.Fatal("rejected receipt changed execution", current)
			}
			next.Steps[0].EvidenceID = "original-evidence"
			if !CheckpointProgress(original, next) {
				t.Fatal("valid confirmation rejected")
			}
		})
	}
}

func TestStateEventsEnforcePersistenceAndImmutableRequest(t *testing.T) {
	for _, test := range []struct {
		name, from, to, event string
		allowed               bool
	}{
		{"approval", "awaiting_approval", "approved", "approve", true},
		{"queue", "approved", "queued", "dispatch", true},
		{"degraded_execution", "queued", "degraded_running", "start", true},
		{"uncertain_feedback", "running", "result_unknown", "finish", true},
		{"cancel_unknown", "result_unknown", "cancelled_result_unknown", "cancel", true},
		{"reconcile_before_resume", "result_unknown", "ready_to_resume", "reconcile", true},
		{"resume_remaining", "ready_to_resume", "queued", "resume", true},
		{"late_cancel_feedback", "cancelled_result_unknown", "cancelled", "step", true},
		{"unapproved_dispatch", "awaiting_approval", "queued", "dispatch", false},
		{"automatic_unknown_restart", "result_unknown", "running", "start", false},
		{"resume_unknown", "result_unknown", "queued", "resume", false},
		{"completed_restart", "completed", "running", "start", false},
		{"cancelled_resume", "cancelled", "queued", "resume", false},
		{"unknown_event", "approved", "queued", "arbitrary", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, _, _ := fixture(t)
			ctx := context.Background()
			original := model.Execution{DownlinkID: test.name, DefinitionID: "plan", DefinitionVersion: 1, Params: map[string]string{"value": "original"}, Status: test.from, Version: 1}
			if _, err := s.Identity.Store.Put(ctx, "execution", original.DownlinkID, 0, original); err != nil {
				t.Fatal(err)
			}
			next := original
			next.Status = test.to
			err := s.Store.Write(ctx, func(tx *store.Tx) error {
				return ApplyTransition(tx, &next, 1, test.event, TransitionOptions{ReceiptNode: "edge-a"})
			})
			if test.allowed && err != nil || !test.allowed && !errors.Is(err, ErrTransition) {
				t.Fatal(err)
			}
			current, _ := s.Get(ctx, original.DownlinkID)
			transitions, _ := s.Store.ExecutionTransitions(ctx, original.DownlinkID)
			audits, _ := s.Store.AuditList(ctx, original.DownlinkID, 100)
			if test.allowed {
				if current.Version != 2 || current.Status != test.to || len(transitions) != 1 || len(audits) != 1 {
					t.Fatal(current, transitions, audits)
				}
			} else if current.Version != 1 || current.Status != test.from || len(transitions) != 0 || len(audits) != 0 {
				t.Fatal("invalid transition altered durable state", current, transitions, audits)
			}
			changed := current
			changed.Params = map[string]string{"value": "changed"}
			if err = s.Store.Write(ctx, func(tx *store.Tx) error {
				return ApplyTransition(tx, &changed, current.Version, "receipt", TransitionOptions{})
			}); !errors.Is(err, store.ErrConflict) {
				t.Fatal("request identity changed", err)
			}
		})
	}
}

func TestProcessRecoveryFaultPreservesReservedCommand(t *testing.T) {
	s, users, _ := fixture(t)
	d := deviceEvidence(s)
	req := queued(t, s, users, "recovery-interrupted")
	crash := errors.New("recovery injected exit")
	s.Fault = func(_ context.Context, phase string, _ model.Execution, _ string) error {
		if phase == "after_reserve" || phase == "process_recovery" {
			return crash
		}
		return nil
	}
	if _, err := s.Run(context.Background(), req, false); !errors.Is(err, crash) {
		t.Fatal(err)
	}
	before, _ := s.Get(context.Background(), req.DownlinkID)
	if _, err := s.Run(context.Background(), req, false); !errors.Is(err, crash) {
		t.Fatal(err)
	}
	after, _ := s.Get(context.Background(), req.DownlinkID)
	if after.Version != before.Version || len(d.counts()) != 0 {
		t.Fatal(after, before, d.counts())
	}
	s.Fault = nil
	final, err := s.Run(context.Background(), req, false)
	if err != nil || final.Status != "result_unknown" || len(d.counts()) != 0 {
		t.Fatal(final, err, d.counts())
	}
	assertAtomicHistory(t, s, final)
	t.Log("process_recovery injected failure retained reserved command; recovery ended result_unknown, physical actions 0")
}
