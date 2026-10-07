package command

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/state"
)

func TestReadOnlyResultUsesOriginalJournalAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "results.db")
	journal, err := state.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	exec := &fakeExecutor{}
	s := NewService(time.Minute)
	s.SetJournal(journal)
	s.SetResolver(fakeResolver{executor: exec, ok: true})
	command := testControlCommand("核对:甲:step-a")
	if _, err = s.Handle(ctx, command); err != nil {
		t.Fatal(err)
	}
	original, err := s.CommandResult(ctx, command.CommandId)
	if err != nil || !original.Found || !original.BindingKnown || original.Response.Status != dt.CommandStatus_SUCCESS || original.RecordedAtMs == 0 || len(original.RequestSha256) != 32 {
		t.Fatal(original, err)
	}
	s.Close()
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err = state.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	restored := NewService(time.Minute)
	defer restored.Close()
	restored.SetJournal(journal)
	restored.SetResolver(fakeResolver{executor: exec, ok: true})
	for i := 0; i < 3; i++ {
		result, err := restored.CommandResult(ctx, command.CommandId)
		if err != nil || result.RecordedAtMs != original.RecordedAtMs || result.Response.Status != dt.CommandStatus_SUCCESS || !result.BindingKnown {
			t.Fatal(result, err)
		}
	}
	missing, err := restored.CommandResult(ctx, "absent")
	if err != nil || missing.Found {
		t.Fatal(missing, err)
	}
	if exec.calls != 1 {
		t.Fatal("query caused physical command", exec.calls)
	}
	t.Logf("original command=%s timestamp=%d source=%s queries=4 physical_actions=%d", command.CommandId, original.RecordedAtMs, original.Source, exec.calls)
}
func TestReadOnlyResultDoesNotStartReservedCommand(t *testing.T) {
	ctx := context.Background()
	journal, err := state.Open(ctx, filepath.Join(t.TempDir(), "reserved.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	command := testControlCommand("reserved-result")
	if _, _, err = journal.Reserve(ctx, command); err != nil {
		t.Fatal(err)
	}
	exec := &fakeExecutor{}
	s := NewService(time.Minute)
	defer s.Close()
	s.SetJournal(journal)
	s.SetResolver(fakeResolver{executor: exec, ok: true})
	result, err := s.CommandResult(ctx, command.CommandId)
	if err != nil || !result.Found || result.Response.Status != dt.CommandStatus_RESULT_UNKNOWN || exec.calls != 0 {
		t.Fatal(result, err, exec.calls)
	}
}
