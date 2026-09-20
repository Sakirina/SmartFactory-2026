package command

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/state"
)

func TestPersistentCommandSurvivesRestartAndRejectsConflictingContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commands.db")
	journal, err := state.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	executor := &fakeExecutor{}
	s := NewService(time.Minute)
	s.SetJournal(journal)
	s.SetResolver(fakeResolver{executor: executor, ok: true})
	msg := testControlCommand("persisted")
	if result, err := s.Handle(context.Background(), msg); err != nil || result.Response.Status != dtv1.CommandStatus_SUCCESS {
		t.Fatalf("execute: %+v, %v", result, err)
	}
	s.Close()
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err = state.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	restored := NewService(time.Minute)
	defer restored.Close()
	restored.SetJournal(journal)
	restored.SetResolver(fakeResolver{executor: executor, ok: true})
	result, err := restored.Handle(context.Background(), msg)
	if err != nil || !result.Duplicate || result.Response.Status != dtv1.CommandStatus_SUCCESS || executor.calls != 1 {
		t.Fatalf("after restart: %+v, %v, calls=%d", result, err, executor.calls)
	}
	msg.GetControl().Action = "another_action"
	if _, err := restored.Handle(context.Background(), msg); !errors.Is(err, state.ErrConflict) {
		t.Fatalf("conflicting command error = %v", err)
	}
}

func TestInterruptedCommandNeverReexecutes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commands.db")
	journal, err := state.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	msg := testControlCommand("interrupted")
	if _, _, err := journal.Reserve(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	_ = journal.Close()
	journal, err = state.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	executor := &fakeExecutor{}
	s := NewService(time.Minute)
	defer s.Close()
	s.SetJournal(journal)
	s.SetResolver(fakeResolver{executor: executor, ok: true})
	result, err := s.Handle(context.Background(), msg)
	if err != nil || !result.Duplicate || result.Response.Status != dtv1.CommandStatus_RESULT_UNKNOWN || executor.calls != 0 {
		t.Fatalf("interrupted result = %+v, %v, calls=%d", result, err, executor.calls)
	}
}

func TestNonIdempotentFailureIsNotRetriedAndExpiredCommandDoesNotStart(t *testing.T) {
	executor := &fakeExecutor{fn: func(context.Context, *dtv1.DeviceMessage) (*dtv1.CommandResponsePayload, error) {
		return nil, errors.New("connection lost after sending")
	}}
	s := NewService(time.Minute)
	defer s.Close()
	s.SetResolver(fakeResolver{executor: executor, ok: true})
	msg := testControlCommand("non-idempotent")
	msg.GetControl().Options = &dtv1.CommandOptions{RetryCount: 10}
	result, err := s.Handle(context.Background(), msg)
	if err != nil || result.Response.Status != dtv1.CommandStatus_RESULT_UNKNOWN || executor.calls != 1 {
		t.Fatalf("unsafe retry: %+v, %v, calls=%d", result, err, executor.calls)
	}
	msg = testControlCommand("expired")
	msg.GetControl().Options = &dtv1.CommandOptions{StartDeadlineMs: time.Now().Add(-time.Second).UnixMilli()}
	result, err = s.Handle(context.Background(), msg)
	if err != nil || result.Response.Status != dtv1.CommandStatus_REJECTED || executor.calls != 1 {
		t.Fatalf("expired command executed: %+v, %v, calls=%d", result, err, executor.calls)
	}
}

func TestPersistenceFailurePreventsDeviceIO(t *testing.T) {
	journal, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "closed.db"))
	if err != nil {
		t.Fatal(err)
	}
	_ = journal.Close()
	executor := &fakeExecutor{}
	s := NewService(time.Minute)
	defer s.Close()
	s.SetJournal(journal)
	s.SetResolver(fakeResolver{executor: executor, ok: true})
	if _, err := s.Handle(context.Background(), testControlCommand("must-not-run")); err == nil || executor.calls != 0 {
		t.Fatalf("storage failure allowed command: %v, calls=%d", err, executor.calls)
	}
}
