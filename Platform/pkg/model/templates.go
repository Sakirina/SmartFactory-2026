package model

type TemplateInstance struct {
	ID                   string         `json:"id" required:"true"`
	Name                 string         `json:"name" required:"true"`
	TemplateID           string         `json:"template_id" required:"true"`
	TemplateVersion      int64          `json:"template_version" required:"true"`
	DeviceID             string         `json:"device_id" required:"true"`
	DeviceVersion        int64          `json:"device_version" minimum:"1" required:"true"`
	ConfigurationID      string         `json:"configuration_id" required:"true"`
	ConfigurationVersion int64          `json:"configuration_version" minimum:"1" required:"true"`
	SafetyUserID         string         `json:"safety_user_id,omitempty"`
	Parameters           map[string]any `json:"parameters,omitempty"`
}

type TemplateBatchInput struct {
	ID        string             `json:"id" required:"true"`
	RequestID string             `json:"request_id" required:"true"`
	GroupID   string             `json:"group_id" required:"true"`
	Instances []TemplateInstance `json:"instances" minItems:"1" maxItems:"20" required:"true"`
}

type TemplateFailure struct {
	InstanceID string `json:"instance_id"`
	Code       string `json:"code"`
	Message    string `json:"message"`
}

type TemplateBinding struct {
	InstanceID           string `json:"instance_id"`
	DeviceID             string `json:"device_id"`
	DeviceVersion        int64  `json:"device_version"`
	ConfigurationID      string `json:"configuration_id"`
	ConfigurationVersion int64  `json:"configuration_version"`
	AppliedVersion       int64  `json:"applied_version"`
	ApplicationStatus    string `json:"application_status"`
	SourceID             string `json:"source_id"`
}

type TemplateBatchAttempt struct {
	AtMS     int64             `json:"at_ms"`
	Actor    Actor             `json:"actor"`
	Status   string            `json:"status"`
	Failures []TemplateFailure `json:"failures"`
}

type TemplateBatch struct {
	ID          string                 `json:"id"`
	GroupID     string                 `json:"group_id"`
	Status      string                 `json:"status"`
	Version     int64                  `json:"version"`
	CreatedMS   int64                  `json:"created_ms"`
	UpdatedMS   int64                  `json:"updated_ms"`
	CreatedBy   string                 `json:"created_by"`
	Input       TemplateBatchInput     `json:"input"`
	RequestHash string                 `json:"request_hash"`
	Drafts      []Draft                `json:"drafts"`
	Bindings    []TemplateBinding      `json:"bindings"`
	Failures    []TemplateFailure      `json:"failures"`
	Attempts    []TemplateBatchAttempt `json:"attempts"`
}
