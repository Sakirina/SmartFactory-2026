package control

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func TestMigrationFourExecutionAndActionJournalRemainRecoverable(t *testing.T) {
	ctx := context.Background()
	s, users, _ := fixture(t)
	twoSteps(t, s)
	d := deviceEvidence(s)
	d.uncertain = true
	req := queued(t, s, users, "legacy:existing-execution")
	req, err := s.Run(ctx, req, false)
	if err != nil {
		t.Fatal(err)
	}
	// Build a migration-four sample using the old execution field set and its
	// original action table. The old schema never had transition or evidence rows.
	raw, _ := json.Marshal(req)
	var fields map[string]any
	json.Unmarshal(raw, &fields)
	for _, key := range []string{"mode", "branch", "active_command_id", "cancel_requested_ms", "cancel_actor", "cancel_reason", "resume_actor", "reconciled_ms", "resource_binding"} {
		delete(fields, key)
	}
	raw, _ = json.Marshal(fields)
	if _, err = s.Identity.Store.DB.Exec("UPDATE documents SET data=$1 WHERE kind='execution' AND id=$2", string(raw), req.DownlinkID); err != nil {
		t.Fatal(err)
	}
	legacyVersion := req.Version
	if err = s.Store.Write(ctx, func(tx *store.Tx) error {
		return tx.Audit(req.Actor, "control.executed", req.DefinitionID, req.DownlinkID, fields)
	}); err != nil {
		t.Fatal(err)
	}
	// Older reserved records could lack their result timestamps. Preserve zeros.
	var action store.ControlAction
	action, err = s.Store.ControlAction(ctx, req.DownlinkID+":step-a")
	if err != nil {
		t.Fatal(err)
	}
	action.Result.StartedMS, action.Result.FinishedMS = 0, 0
	actionRaw, _ := json.Marshal(action.Result)
	if _, err = s.Identity.Store.DB.Exec("UPDATE action_journal SET data=$1,updated_ms=0 WHERE command_id=$2", string(actionRaw), action.CommandID); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"DROP TABLE execution_transitions", "DROP TABLE control_evidence",
		"DROP TABLE sf_analysis_snapshots", "DROP TABLE sf_analysis_steps", "DROP TABLE sf_shadow_inputs", "DROP TABLE sf_analysis_formal",
		"DROP TABLE sf_query_resources", "DROP TABLE sf_query_rows", "DROP TABLE sf_query_commits", "DROP TABLE sf_query_state",
		"DROP TABLE sf_business_requests", "DROP TABLE sf_business_source_sequence", "DROP TABLE sf_alarm_operation_sources",
		"DELETE FROM sf_schema_migrations WHERE version>4", "DELETE FROM goose_db_version WHERE version_id>4",
	} {
		if _, err = s.Identity.Store.DB.Exec(query); err != nil {
			t.Fatal(query, err)
		}
	}
	var seq int
	var name, path string
	if err = s.Identity.Store.DB.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	clock := s.Identity.Store.Now
	if err = s.Identity.Store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(ctx, path, s.NodeID, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	reopened.Now = clock
	s.Store, s.Identity.Store, s.Definitions.Store = reopened, reopened, reopened
	d.now = reopened.CurrentTime
	detail, err := s.Detail(ctx, users["engineer"], req.DownlinkID)
	if err != nil || detail.Execution.Mode != "" || detail.Execution.ResourceBinding != "" || len(detail.Evidence) != 0 {
		t.Fatal(detail, err)
	}
	legacyAction, legacyAudit, canReconcile := false, false, false
	for _, event := range detail.Timeline {
		if event.Kind == "transition" {
			t.Fatal("migration fabricated prior state transitions", event)
		}
		if event.Source == "action_journal" {
			legacyAction = true
			if event.AtMS != 0 || event.Step.StartedMS != 0 || event.Step.FinishedMS != 0 {
				t.Fatal("missing legacy time was fabricated", event)
			}
		}
		legacyAudit = legacyAudit || event.Kind == "legacy_audit" && event.Message == "control.executed"
	}
	for _, a := range detail.AllowedActions {
		canReconcile = canReconcile || a.Action == "reconcile" && a.Allowed
	}
	if !legacyAction || !legacyAudit || !canReconcile {
		t.Fatal(legacyAction, legacyAudit, detail.AllowedActions)
	}
	if _, err = s.Run(ctx, detail.Execution, false); err != nil || d.counts()[action.CommandID] != 1 {
		t.Fatal("old unknown execution was automatically resent", err, d.counts())
	}
	req, err = s.Reconcile(ctx, users["engineer"], req.DownlinkID, model.ExecutionAction{ExpectedVersion: legacyVersion, Reason: "核对旧版动作日志"})
	if err != nil || req.Status != "ready_to_resume" {
		t.Fatal(req, err)
	}
	if _, err = s.Resume(ctx, users["engineer"], req.DownlinkID, model.ExecutionAction{ExpectedVersion: legacyVersion, Reason: "旧版本请求"}); !errors.Is(err, store.ErrConflict) {
		t.Fatal(err)
	}
	req, err = s.Resume(ctx, users["engineer"], req.DownlinkID, model.ExecutionAction{ExpectedVersion: req.Version, Reason: "继续旧记录的剩余步骤"})
	if err != nil {
		t.Fatal(err)
	}
	d.uncertain = false
	req, err = s.Run(ctx, req, false)
	if err != nil || req.Status != "completed" || d.counts()[action.CommandID] != 1 || d.counts()[req.DownlinkID+":step-b"] != 1 {
		t.Fatal(req, err, d.counts())
	}
	transitions, err := s.Store.ExecutionTransitions(ctx, req.DownlinkID)
	if err != nil || len(transitions) == 0 || transitions[0].Version != legacyVersion+1 {
		t.Fatal(transitions, err)
	}
	t.Logf("migration-four sample reopened; old missing action times retained as 0; first recorded transition=%d, old_version=%d, manual result=%s, counts=%v", transitions[0].Version, legacyVersion, req.Status, d.counts())
}
