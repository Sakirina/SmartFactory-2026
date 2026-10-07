package model

// BusinessAction describes the result of the same authorization and state rules
// used when the corresponding application operation is submitted.
type BusinessAction struct {
	Action  string `json:"action"`
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

type AlarmOperation struct {
	ID                 string   `json:"id"`
	AlarmID            string   `json:"alarm_id"`
	EntityID           string   `json:"entity_id"`
	DefinitionID       string   `json:"definition_id"`
	SourceID           string   `json:"source_id"`
	SourceSequence     int64    `json:"source_sequence"`
	Version            int64    `json:"version"`
	AlarmVersion       int64    `json:"alarm_version"`
	AlarmActionVersion string   `json:"alarm_action_version"`
	CaseVersion        int64    `json:"case_version"`
	Action             string   `json:"action"`
	Reason             string   `json:"reason"`
	AssigneeID         string   `json:"assignee_id,omitempty"`
	Actor              Actor    `json:"actor"`
	AtMS               int64    `json:"at_ms"`
	Basis              []string `json:"basis"`
}

type AlarmCase struct {
	ID             string           `json:"id"`
	AlarmID        string           `json:"alarm_id"`
	EntityID       string           `json:"entity_id"`
	DefinitionID   string           `json:"definition_id"`
	Version        int64            `json:"version"`
	Acknowledged   bool             `json:"acknowledged"`
	AcknowledgedBy string           `json:"acknowledged_by,omitempty"`
	AcknowledgedMS int64            `json:"acknowledged_ms,omitempty"`
	AssigneeID     string           `json:"assignee_id,omitempty"`
	Status         string           `json:"status"`
	UpdatedMS      int64            `json:"updated_ms"`
	CompletedMS    int64            `json:"completed_ms,omitempty"`
	ConflictFields []string         `json:"conflict_fields"`
	PendingBasis   []string         `json:"pending_basis"`
	Operations     []AlarmOperation `json:"operations"`
}

type AlarmDetail struct {
	Alarm          Alarm                 `json:"alarm"`
	ActionVersion  string                `json:"action_version"`
	Case           AlarmCase             `json:"case"`
	Native         *NativeAlarmState     `json:"native,omitempty"`
	NativeSync     NativeAlarmSyncStatus `json:"native_sync"`
	AllowedActions []BusinessAction      `json:"allowed_actions"`
}

type BusinessEntry struct {
	ID            string `json:"id"`
	Action        string `json:"action"`
	Reason        string `json:"reason"`
	Actor         Actor  `json:"actor"`
	AtMS          int64  `json:"at_ms"`
	BeforeVersion int64  `json:"before_version"`
	Version       int64  `json:"version"`
}

type WorkOrder struct {
	ID                   string          `json:"id"`
	Title                string          `json:"title"`
	Description          string          `json:"description"`
	GroupID              string          `json:"group_id"`
	Status               string          `json:"status"`
	AssigneeID           string          `json:"assignee_id"`
	AssigneeDepartmentID string          `json:"assignee_department_id"`
	AssigneeVersion      int64           `json:"assignee_version"`
	AlarmIDs             []string        `json:"alarm_ids"`
	ExecutionIDs         []string        `json:"execution_ids"`
	CreatedBy            string          `json:"created_by"`
	CreatedMS            int64           `json:"created_ms"`
	UpdatedMS            int64           `json:"updated_ms"`
	CompletedMS          int64           `json:"completed_ms,omitempty"`
	Version              int64           `json:"version"`
	Entries              []BusinessEntry `json:"entries"`
}

type HandoverEvidence struct {
	Kind          string `json:"kind" enum:"alarm,execution,work_order" required:"true"`
	ID            string `json:"id" required:"true"`
	Version       int64  `json:"version" required:"true"`
	ActionVersion string `json:"action_version,omitempty"`
	Description   string `json:"description,omitempty"`
}

type Handover struct {
	ID               string             `json:"id"`
	WorkOrderID      string             `json:"work_order_id"`
	GroupID          string             `json:"group_id"`
	FromUserID       string             `json:"from_user_id"`
	ToUserID         string             `json:"to_user_id"`
	FromDepartmentID string             `json:"from_department_id"`
	ToDepartmentID   string             `json:"to_department_id"`
	PendingItems     []string           `json:"pending_items"`
	Evidence         []HandoverEvidence `json:"evidence"`
	Reason           string             `json:"reason"`
	Actor            Actor              `json:"actor"`
	AtMS             int64              `json:"at_ms"`
	WorkOrderVersion int64              `json:"work_order_version"`
	Version          int64              `json:"version"`
}

type WorkOrderDetail struct {
	WorkOrder           WorkOrder        `json:"work_order"`
	Handovers           []Handover       `json:"handovers"`
	AllowedActions      []BusinessAction `json:"allowed_actions"`
	ResponsibilityIssue string           `json:"responsibility_issue,omitempty"`
}
