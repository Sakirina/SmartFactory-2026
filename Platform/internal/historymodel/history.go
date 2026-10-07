// Package historymodel contains durable analysis records without store or worker dependencies.
package historymodel

import (
	"competition2026/product/platform/internal/rulecore"
	"competition2026/product/platform/pkg/model"
)

const MaxPoints = 10000
const MaxHistory = 40000
const MaxSnapshotBytes = 32 << 20

type HistoryClock struct {
	AtMS          *int64 `json:"at_ms,omitempty"`
	StepMS        int64  `json:"step_ms,omitempty"`
	FreshnessAtMS *int64 `json:"freshness_at_ms,omitempty"`
	FreshnessMS   *int64 `json:"freshness_ms,omitempty"`
}

type HistoryState map[string]map[string]rulecore.RuntimeState

type HistoryRequest struct {
	ID            string               `json:"id,omitempty"`
	Kind          string               `json:"kind" enum:"replay,compare"`
	DefinitionID  string               `json:"definition_id"`
	LeftVersion   int64                `json:"left_version,omitempty"`
	RightVersion  int64                `json:"right_version,omitempty"`
	FromMS        int64                `json:"from_ms"`
	ToMS          int64                `json:"to_ms"`
	DeviceIDs     []string             `json:"device_ids,omitempty"`
	Keys          []string             `json:"keys,omitempty"`
	Points        []model.Observation  `json:"points,omitempty"`
	History       []model.Observation  `json:"history,omitempty"`
	AssetVersions []model.AssetVersion `json:"asset_versions,omitempty"`
	InitialState  State                `json:"initial_state,omitempty"`
	Order         string               `json:"order,omitempty" enum:"event_time,provided"`
	Clock         Clock                `json:"clock,omitempty"`
}

type HistoryRule struct {
	Definition model.Definition     `json:"definition"`
	Plan       *model.ExecutionPlan `json:"plan,omitempty"`
}

type HistorySnapshot struct {
	ID                 string               `json:"id"`
	SHA256             string               `json:"sha256"`
	InputSHA256        string               `json:"input_sha256"`
	HistorySHA256      string               `json:"history_sha256"`
	CapturedMS         int64                `json:"captured_ms"`
	FromMS             int64                `json:"from_ms"`
	ToMS               int64                `json:"to_ms"`
	Source             string               `json:"source"`
	DataVersion        int64                `json:"data_version"`
	DataGaps           []model.DataGap      `json:"data_gaps"`
	Completeness       string               `json:"completeness"`
	Order              string               `json:"order"`
	Clock              Clock                `json:"clock"`
	Points             []model.Observation  `json:"points"`
	History            []model.Observation  `json:"history"`
	AssetVersions      []model.AssetVersion `json:"asset_versions"`
	InitialState       State                `json:"initial_state"`
	InitialStateSource string               `json:"initial_state_source"`
	InitialStateRunID  string               `json:"initial_state_run_id,omitempty"`
	Rules              []Rule               `json:"rules"`
	Resources          []string             `json:"resources"`
}

type HistoryRun struct {
	ID                   string           `json:"id"`
	Kind                 string           `json:"kind"`
	DefinitionID         string           `json:"definition_id"`
	LeftVersion          int64            `json:"left_version,omitempty"`
	RightVersion         int64            `json:"right_version,omitempty"`
	SnapshotID           string           `json:"snapshot_id"`
	CapturedSnapshotID   string           `json:"captured_snapshot_id"`
	SnapshotSHA256       string           `json:"snapshot_sha256"`
	InputSHA256          string           `json:"input_sha256"`
	TaskID               string           `json:"task_id"`
	Status               string           `json:"status"`
	Error                string           `json:"error,omitempty"`
	Cursor               int              `json:"cursor"`
	Total                int              `json:"total"`
	Progress             float64          `json:"progress"`
	FinalState           map[string]State `json:"final_state"`
	Resources            []string         `json:"resources"`
	CreatedMS            int64            `json:"created_ms"`
	UpdatedMS            int64            `json:"updated_ms"`
	Version              int64            `json:"version"`
	CandidateID          string           `json:"candidate_id,omitempty"`
	CandidateVersion     int64            `json:"candidate_version,omitempty"`
	InputGeneration      int64            `json:"input_generation,omitempty"`
	ParentRunID          string           `json:"parent_run_id,omitempty"`
	InitialStateResolved bool             `json:"initial_state_resolved"`
}

type HistoryLane struct {
	Side              string              `json:"side"`
	DefinitionVersion int64               `json:"definition_version"`
	EffectiveMS       int64               `json:"effective_ms"`
	PlanID            string              `json:"plan_id,omitempty"`
	PlanSHA256        string              `json:"plan_sha256,omitempty"`
	Skipped           string              `json:"skipped,omitempty"`
	Evaluation        rulecore.Evaluation `json:"evaluation"`
	ActionIntents     []model.Step        `json:"action_intents"`
}

type HistoryDifference struct {
	Changed bool `json:"changed"`
	Values  bool `json:"values"`
	Quality bool `json:"quality"`
	Alarm   bool `json:"alarm"`
	State   bool `json:"state"`
	Path    bool `json:"path"`
	Trigger bool `json:"trigger"`
}

type HistoryStep struct {
	ID               string               `json:"id"`
	RunID            string               `json:"run_id"`
	InputIndex       int                  `json:"input_index"`
	InputSHA256      string               `json:"input_sha256"`
	Point            model.Observation    `json:"point"`
	ClockMS          int64                `json:"clock_ms"`
	FreshnessAtMS    int64                `json:"freshness_at_ms"`
	Lanes            []Lane               `json:"lanes"`
	Difference       Difference           `json:"difference"`
	FormalOutputs    []model.Observation  `json:"formal_outputs"`
	FormalInputID    string               `json:"formal_input_id,omitempty"`
	FormalStatus     string               `json:"formal_status,omitempty"`
	FormalEvaluation *rulecore.Evaluation `json:"formal_evaluation,omitempty"`
	FormalDifference Difference           `json:"formal_difference"`
}

type HistoryRunList struct {
	Items []Run  `json:"items"`
	Next  string `json:"next"`
}
type HistoryStepList struct {
	Items []Step `json:"items"`
	Next  int    `json:"next"`
}

type ShadowRequest struct {
	ID              string   `json:"id"`
	DefinitionID    string   `json:"definition_id"`
	Version         int64    `json:"version"`
	ExpectedVersion int64    `json:"expected_version,omitempty"`
	DeviceIDs       []string `json:"device_ids,omitempty"`
	Keys            []string `json:"keys,omitempty"`
	InitialState    State    `json:"initial_state,omitempty"`
}

type ShadowCandidate struct {
	ID                  string              `json:"id"`
	DefinitionID        string              `json:"definition_id"`
	DefinitionVersion   int64               `json:"definition_version"`
	Version             int64               `json:"version"`
	Epoch               int64               `json:"epoch"`
	Status              string              `json:"status"`
	Error               string              `json:"error,omitempty"`
	DeviceIDs           []string            `json:"device_ids"`
	Keys                []string            `json:"keys"`
	Resources           []string            `json:"resources"`
	StartedMS           int64               `json:"started_ms"`
	StoppedMS           int64               `json:"stopped_ms,omitempty"`
	Generation          int64               `json:"generation"`
	CompletedGeneration int64               `json:"completed_generation"`
	LastRunID           string              `json:"last_run_id,omitempty"`
	InputCount          int                 `json:"input_count"`
	LastInput           *model.Observation  `json:"last_input,omitempty"`
	Context             []model.Observation `json:"context"`
	BaseSnapshot        Snapshot            `json:"base_snapshot"`
}

type ShadowCandidateList struct {
	Items []Candidate `json:"items"`
}
type ShadowCandidateAction struct {
	ExpectedVersion int64 `json:"expected_version"`
}

// One queue argument shape lets both SQL implementations preserve task identity.
type TaskArgs struct {
	ID    string `json:"id"`
	RunID string `json:"run_id"`
}

func (TaskArgs) Kind() string { return "smartfactory_analysis" }

type Clock = HistoryClock

type State = HistoryState

type Request = HistoryRequest

type Rule = HistoryRule

type Snapshot = HistorySnapshot

type Run = HistoryRun

type Lane = HistoryLane

type Difference = HistoryDifference

type Step = HistoryStep

type RunList = HistoryRunList

type StepList = HistoryStepList

type Candidate = ShadowCandidate

type CandidateList = ShadowCandidateList

type CandidateAction = ShadowCandidateAction
