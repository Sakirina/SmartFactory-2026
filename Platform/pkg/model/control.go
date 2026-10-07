package model

// ExecutionTransition describes one committed local document revision. SourceVersion
// is the remote revision when a cloud receipt advances an independent local version.
type ExecutionTransition struct {
	ExecutionID     string   `json:"execution_id"`
	Version         int64    `json:"version"`
	PreviousVersion int64    `json:"previous_version"`
	Event           string   `json:"event"`
	From            string   `json:"from"`
	To              string   `json:"to"`
	AtMS            int64    `json:"at_ms"`
	Source          string   `json:"source"`
	SourceVersion   int64    `json:"source_version,omitempty"`
	Actor           Actor    `json:"actor"`
	Reason          string   `json:"reason,omitempty"`
	CommandID       string   `json:"command_id,omitempty"`
	EvidenceIDs     []string `json:"evidence_ids,omitempty"`
	TraceID         string   `json:"trace_id,omitempty"`
}

// CommandEvidence records a journal lookup or a late original command response.
// ObservedMS belongs to the named source; CollectedMS is the platform capture time.
type CommandEvidence struct {
	ID          string `json:"id"`
	ExecutionID string `json:"execution_id"`
	CommandID   string `json:"command_id"`
	DeviceID    string `json:"device_id"`
	StepID      string `json:"step_id"`
	Source      string `json:"source"`
	ObservedMS  int64  `json:"observed_ms"`
	CollectedMS int64  `json:"collected_ms"`
	PayloadHash string `json:"payload_hash"`
	ContentHash string `json:"content_hash"`
	RequestHash string `json:"request_hash,omitempty"`
	Status      string `json:"status"`
	Message     string `json:"message,omitempty"`
	Trusted     bool   `json:"trusted"`
	Rejection   string `json:"rejection,omitempty"`
	Actor       Actor  `json:"actor"`
	TraceID     string `json:"trace_id,omitempty"`
}

// ExecutionReceipt keeps the execution fields at the root for stored v1
// deliveries and older peers. Every new revision includes all immutable evidence
// so receiving a later revision first cannot omit its earlier command feedback.
type ExecutionReceipt struct {
	Execution
	ReceiptSource   *ExecutionReceiptSource `json:"receipt_source,omitempty"`
	CommandEvidence []CommandEvidence       `json:"command_evidence,omitempty"`
}

type ExecutionReceiptSource struct {
	SchemaVersion int    `json:"schema_version"`
	NodeID        string `json:"node_id"`
	Version       int64  `json:"version"`
	RecordedMS    int64  `json:"recorded_ms"`
}

type ExecutionAction struct {
	ExpectedVersion       int64  `json:"expected_version" minimum:"1"`
	Reason                string `json:"reason" minLength:"1" maxLength:"2000"`
	OperationID           string `json:"operation_id,omitempty" maxLength:"200"`
	ExpectedSourceVersion *int64 `json:"expected_source_version,omitempty" minimum:"0"`
	DeadlineMS            int64  `json:"deadline_ms,omitempty"`
}

type ExecutionAllowedAction struct {
	Action  string `json:"action"`
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

// Timeline entries retain the original source. Legacy records have only the
// times actually present in their audit or action records.
type ExecutionTimelineEntry struct {
	ID         string               `json:"id"`
	Kind       string               `json:"kind"`
	Source     string               `json:"source"`
	AtMS       int64                `json:"at_ms"`
	Transition *ExecutionTransition `json:"transition,omitempty"`
	Approval   *Approval            `json:"approval,omitempty"`
	Step       *StepResult          `json:"step,omitempty"`
	Evidence   *CommandEvidence     `json:"evidence,omitempty"`
	Actor      Actor                `json:"actor"`
	TraceID    string               `json:"trace_id,omitempty"`
	Message    string               `json:"message,omitempty"`
}

type ExecutionDetail struct {
	Execution      Execution                `json:"execution"`
	AllowedActions []ExecutionAllowedAction `json:"allowed_actions"`
	Timeline       []ExecutionTimelineEntry `json:"timeline"`
	Evidence       []CommandEvidence        `json:"evidence"`
	Operations     []ControlOperation       `json:"operations"`
	SourceNodeID   string                   `json:"source_node_id"`
	SourceVersion  int64                    `json:"source_version"`
}

type ControlOperation struct {
	ID                    string     `json:"id"`
	ExecutionID           string     `json:"execution_id"`
	Action                string     `json:"action"`
	Status                string     `json:"status"`
	OriginNodeID          string     `json:"origin_node_id"`
	TargetNodeID          string     `json:"target_node_id"`
	ExpectedVersion       int64      `json:"expected_version"`
	ExpectedSourceVersion int64      `json:"expected_source_version"`
	RequestedMS           int64      `json:"requested_ms"`
	DeadlineMS            int64      `json:"deadline_ms"`
	StartedMS             int64      `json:"started_ms,omitempty"`
	ProcessedMS           int64      `json:"processed_ms,omitempty"`
	Reason                string     `json:"reason"`
	Actor                 Actor      `json:"actor"`
	RequestHash           string     `json:"request_hash"`
	Error                 string     `json:"error,omitempty"`
	ResultVersion         int64      `json:"result_version,omitempty"`
	Result                *Execution `json:"result,omitempty"`
	Version               int64      `json:"version"`
	TraceID               string     `json:"trace_id,omitempty"`
}

type ControlOperationEnvelope struct {
	Operation ControlOperation `json:"operation"`
	Execution Execution        `json:"execution"`
}
