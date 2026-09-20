// Package model contains the versioned contracts shared by cloud and edge.
package model

import "encoding/json"

const ContractVersion = "1.0"

type DashboardMetric struct {
	DeviceID string `json:"device_id"`
	Key      string `json:"key"`
	Label    string `json:"label"`
}

type Dashboard struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	GroupID     string            `json:"group_id"`
	Version     int64             `json:"version"`
	DeviceIDs   []string          `json:"device_ids"`
	Keys        []string          `json:"keys"`
	Metrics     []DashboardMetric `json:"metrics"`
	WindowMS    int64             `json:"window_ms"`
	RefreshMS   int64             `json:"refresh_ms"`
	ShowSources bool              `json:"show_sources"`
	ShowAlarms  bool              `json:"show_alarms"`
}

type Entity struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Kind       string            `json:"kind"`
	ParentID   string            `json:"parent_id,omitempty"`
	EdgeID     string            `json:"edge_id,omitempty"`
	Protocol   string            `json:"protocol,omitempty"`
	Status     string            `json:"status"`
	Version    int64             `json:"version"`
	Tags       map[string]string `json:"tags,omitempty"`
	SamplingMS int64             `json:"sampling_ms,omitempty"`
	Config     json.RawMessage   `json:"config,omitempty"`
	TBID       string            `json:"tb_id,omitempty"`
}

type Observation struct {
	ID             string `json:"id"`
	OriginID       string `json:"origin_id,omitempty"`
	MessageID      string `json:"message_id"`
	SourceID       string `json:"source_id"`
	SourceSequence uint64 `json:"source_sequence"`
	DeviceID       string `json:"device_id"`
	Key            string `json:"key"`
	Value          any    `json:"value"`
	ObservedMS     int64  `json:"observed_ms"`
	ReceivedMS     int64  `json:"received_ms"`
	Quality        string `json:"quality"`
	QualityReason  string `json:"quality_reason,omitempty"`
	TimeSource     string `json:"time_source"`
	Unit           string `json:"unit,omitempty"`
	EntityRevision int64  `json:"entity_revision"`
	AssetVersion   int64  `json:"asset_version"`
	RuleVersion    int64  `json:"rule_version,omitempty"`
	DefinitionID   string `json:"definition_id,omitempty"`
	Late           bool   `json:"late"`
	Revision       int64  `json:"revision"`
}

type QualitySummary struct {
	Good         int64  `json:"good"`
	Bad          int64  `json:"bad"`
	Uncertain    int64  `json:"uncertain"`
	Excluded     int64  `json:"excluded"`
	Missing      *int64 `json:"missing"`
	Completeness string `json:"completeness"`
}
type SourceState struct {
	ID         string `json:"id"`
	LastSeenMS int64  `json:"last_seen_ms"`
	Status     string `json:"status"`
	Backfill   string `json:"backfill"`
	Reason     string `json:"reason,omitempty"`
}
type DataResult struct {
	Points      []Observation  `json:"points"`
	Latest      []Observation  `json:"latest,omitempty"`
	Quality     QualitySummary `json:"quality"`
	Sources     []SourceState  `json:"sources"`
	Revisions   []Revision     `json:"revisions"`
	DataVersion int64          `json:"data_version"`
	Truncated   bool           `json:"truncated"`
	Gaps        []DataGap      `json:"gaps"`
}
type DataGap struct {
	ID       string `json:"id"`
	DeviceID string `json:"device_id"`
	Key      string `json:"key"`
	FromMS   int64  `json:"from_ms"`
	ToMS     int64  `json:"to_ms"`
	Missing  int64  `json:"missing"`
	Scope    string `json:"scope"`
	Reason   string `json:"reason"`
}
type Revision struct {
	ID       string `json:"id"`
	JobID    string `json:"job_id"`
	DeviceID string `json:"device_id"`
	Key      string `json:"key"`
	AtMS     int64  `json:"at_ms"`
	Before   any    `json:"before"`
	After    any    `json:"after"`
	Reason   string `json:"reason"`
	Version  int64  `json:"version"`
}

type Port struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type AssetProposal struct {
	ID            string `json:"id"`
	NodeID        string `json:"node_id"`
	Entity        Entity `json:"entity"`
	BaseVersion   int64  `json:"base_version"`
	Status        string `json:"status"`
	Reason        string `json:"reason,omitempty"`
	Actor         Actor  `json:"actor"`
	DecisionActor Actor  `json:"decision_actor"`
	CreatedMS     int64  `json:"created_ms"`
	UpdatedMS     int64  `json:"updated_ms"`
	Version       int64  `json:"version"`
}
type Node struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Label    string             `json:"label,omitempty"`
	Inputs   []Port             `json:"inputs,omitempty"`
	Outputs  []Port             `json:"outputs,omitempty"`
	Params   map[string]any     `json:"params"`
	Position map[string]float64 `json:"position,omitempty"`
}
type Connection struct {
	From     string `json:"from"`
	FromPort string `json:"from_port"`
	To       string `json:"to"`
	ToPort   string `json:"to_port"`
}
type Output struct {
	Key         string `json:"key"`
	Type        string `json:"type"`
	Unit        string `json:"unit,omitempty"`
	NodeID      string `json:"node_id"`
	Description string `json:"description,omitempty"`
}
type Definition struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Kind          string       `json:"kind"`
	SchemaVersion string       `json:"schema_version"`
	Version       int64        `json:"version"`
	Status        string       `json:"status"`
	EffectiveMS   int64        `json:"effective_ms"`
	GroupID       string       `json:"group_id"`
	Nodes         []Node       `json:"nodes"`
	Connections   []Connection `json:"connections"`
	Outputs       []Output     `json:"outputs"`
	Dependencies  []string     `json:"dependencies"`
	Selector      Selector     `json:"selector"`
	Policy        Policy       `json:"policy"`
}
type Selector struct {
	DeviceIDs []string `json:"device_ids"`
	AssetID   string   `json:"asset_id,omitempty"`
	Keys      []string `json:"keys"`
	WindowMS  int64    `json:"window_ms,omitempty"`
}
type Policy struct {
	EdgeIDs          []string    `json:"edge_ids,omitempty"`
	Degraded         []Step      `json:"degraded,omitempty"`
	Steps            []Step      `json:"steps,omitempty"`
	Conditions       []Condition `json:"conditions,omitempty"`
	RiskCategory     string      `json:"risk_category,omitempty"`
	RiskLevel        int         `json:"risk_level,omitempty"`
	SafetyUserID     string      `json:"safety_user_id,omitempty"`
	Schedule         *Schedule   `json:"schedule,omitempty"`
	Channels         []string    `json:"channels,omitempty"`
	Recipients       []string    `json:"recipients,omitempty"`
	LateNotification string      `json:"late_notification,omitempty"`
	TimeoutMS        int64       `json:"timeout_ms,omitempty"`
	MemoryBytes      int64       `json:"memory_bytes,omitempty"`
	FreshnessMS      int64       `json:"freshness_ms,omitempty"`
	Watchdog         bool        `json:"watchdog,omitempty"`
}
type Schedule struct {
	EveryMS  int64  `json:"every_ms"`
	NextMS   int64  `json:"next_ms"`
	WindowMS int64  `json:"window_ms"`
	Timezone string `json:"timezone"`
}
type Condition struct {
	DeviceID  string `json:"device_id"`
	Key       string `json:"key"`
	Operator  string `json:"operator"`
	Value     any    `json:"value"`
	MaxAgeMS  int64  `json:"max_age_ms"`
	Interlock bool   `json:"interlock"`
}
type Step struct {
	ID         string            `json:"id"`
	EdgeID     string            `json:"edge_id"`
	DeviceID   string            `json:"device_id"`
	Action     string            `json:"action"`
	Params     map[string]string `json:"params"`
	Idempotent bool              `json:"idempotent"`
	TimeoutMS  int64             `json:"timeout_ms"`
}
type Draft struct {
	ID          string     `json:"id"`
	Definition  Definition `json:"definition"`
	BaseVersion int64      `json:"base_version"`
	AuthorID    string     `json:"author_id"`
	Version     int64      `json:"version"`
	UpdatedMS   int64      `json:"updated_ms"`
}
type Validation struct {
	Valid      bool     `json:"valid"`
	Errors     []string `json:"errors"`
	Order      []string `json:"order"`
	NativePlan any      `json:"native_plan,omitempty"`
}

type User struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Login        string   `json:"login"`
	DepartmentID string   `json:"department_id"`
	ManagerID    string   `json:"manager_id,omitempty"`
	Grade        string   `json:"grade,omitempty"`
	Roles        []string `json:"roles"`
	Teams        []string `json:"teams"`
	Resources    []string `json:"resources"`
	Active       bool     `json:"active"`
	AI           bool     `json:"ai"`
	Version      int64    `json:"version"`
	Email        string   `json:"email,omitempty"`
	Phone        string   `json:"phone,omitempty"`
	PasswordHash string   `json:"-"`
	TOTPSecret   string   `json:"-"`
}
type Actor struct {
	UserID       string   `json:"user_id"`
	Name         string   `json:"name"`
	DepartmentID string   `json:"department_id"`
	Roles        []string `json:"roles"`
	Source       string   `json:"source"`
	SessionID    string   `json:"session_id"`
	AI           bool     `json:"ai"`
}
type Approval struct {
	UserID  string `json:"user_id"`
	Role    string `json:"role"`
	AtMS    int64  `json:"at_ms"`
	Binding string `json:"binding"`
	Local   bool   `json:"local"`
	StepUp  bool   `json:"step_up"`
	Actor   Actor  `json:"actor"`
}
type Execution struct {
	DownlinkID        string            `json:"downlink_id"`
	DefinitionID      string            `json:"definition_id"`
	DefinitionVersion int64             `json:"definition_version"`
	Params            map[string]string `json:"params"`
	Status            string            `json:"status"`
	Override          bool              `json:"override"`
	RiskCategory      string            `json:"risk_category"`
	RiskLevel         int               `json:"risk_level"`
	Binding           string            `json:"binding"`
	CreatedMS         int64             `json:"created_ms"`
	ApprovalExpiresMS int64             `json:"approval_expires_ms"`
	StartDeadlineMS   int64             `json:"start_deadline_ms"`
	Approvals         []Approval        `json:"approvals"`
	Steps             []StepResult      `json:"steps"`
	Reason            string            `json:"reason,omitempty"`
	Snapshot          []Observation     `json:"snapshot"`
	Actor             Actor             `json:"actor"`
	Fence             uint64            `json:"fence"`
	CoordinatorID     string            `json:"coordinator_id,omitempty"`
	Version           int64             `json:"version"`
}
type StepResult struct {
	StepID     string `json:"step_id"`
	CommandID  string `json:"command_id"`
	Status     string `json:"status"`
	Message    string `json:"message"`
	StartedMS  int64  `json:"started_ms"`
	FinishedMS int64  `json:"finished_ms"`
}
type Alarm struct {
	ID                string `json:"id"`
	DefinitionID      string `json:"definition_id"`
	DefinitionVersion int64  `json:"definition_version"`
	EntityID          string `json:"entity_id"`
	Severity          string `json:"severity"`
	Active            bool   `json:"active"`
	Acknowledged      bool   `json:"acknowledged"`
	StartedMS         int64  `json:"started_ms"`
	UpdatedMS         int64  `json:"updated_ms"`
	ClearedMS         int64  `json:"cleared_ms"`
	Count             int64  `json:"count"`
	Historical        bool   `json:"historical"`
	Value             any    `json:"value"`
	Version           int64  `json:"version"`
	RevisionStatus    string `json:"revision_status,omitempty"`
	RevisionReason    string `json:"revision_reason,omitempty"`
}
type Job struct {
	ID           string  `json:"id"`
	Kind         string  `json:"kind"`
	Status       string  `json:"status"`
	FromMS       int64   `json:"from_ms"`
	ToMS         int64   `json:"to_ms"`
	CursorMS     int64   `json:"cursor_ms"`
	DeviceID     string  `json:"device_id"`
	DefinitionID string  `json:"definition_id,omitempty"`
	Progress     float64 `json:"progress"`
	Error        string  `json:"error,omitempty"`
	Reason       string  `json:"reason"`
	Version      int64   `json:"version"`
}
