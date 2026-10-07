package model

import "encoding/json"

// ProgramBuild is embedded in executable build information and pinned by its
// artifact digest. Database compatibility describes the on-disk schema the
// executable can open; deployment never rewrites migration history.
type ProgramBuild struct {
	Program              string   `json:"program" required:"true"`
	Version              string   `json:"version" required:"true"`
	GOOS                 string   `json:"goos" required:"true"`
	GOARCH               string   `json:"goarch" required:"true"`
	MigrationMinimum     int64    `json:"migration_minimum" minimum:"1" required:"true"`
	MigrationMaximum     int64    `json:"migration_maximum" minimum:"1" required:"true"`
	RuleFormats          []string `json:"rule_formats" required:"true"`
	ConfigurationFormats []string `json:"configuration_formats" required:"true"`
	SourceSHA256         string   `json:"source_sha256,omitempty"`
}

type ReleaseDependency struct {
	ID      string `json:"id" required:"true"`
	Version string `json:"version" required:"true"`
	SHA256  string `json:"sha256" required:"true"`
}

type ReleaseComponent struct {
	ID                   string                  `json:"id" required:"true"`
	Kind                 string                  `json:"kind" required:"true" enum:"program,rule,configuration"`
	Version              string                  `json:"version" required:"true"`
	SHA256               string                  `json:"sha256" required:"true"`
	Format               string                  `json:"format" required:"true"`
	DependsOn            []ReleaseDependency     `json:"depends_on"`
	RequiredCapabilities []string                `json:"required_capabilities"`
	TargetNodeIDs        []string                `json:"target_node_ids,omitempty"`
	Build                *ProgramBuild           `json:"build,omitempty"`
	Content              json.RawMessage         `json:"content,omitempty"`
	Configuration        *ConfigurationReference `json:"configuration,omitempty"`
}

type ReleaseTemplateOrigin struct {
	BatchID             string             `json:"batch_id"`
	BatchVersion        int64              `json:"batch_version"`
	PreviousEvolutionID string             `json:"previous_evolution_id,omitempty"`
	Instances           []TemplateInstance `json:"instances"`
	Bindings            []TemplateBinding  `json:"bindings"`
	DefinitionIDs       []string           `json:"definition_ids"`
}

type TemplateEvolution struct {
	ID                 string             `json:"id"`
	SourceBatchID      string             `json:"source_batch_id"`
	SourceBatchVersion int64              `json:"source_batch_version"`
	Release            Release            `json:"release"`
	Instances          []TemplateInstance `json:"instances"`
	Bindings           []TemplateBinding  `json:"bindings"`
	DeviceIDs          []string           `json:"device_ids"`
	NodeIDs            []string           `json:"node_ids"`
	DefinitionIDs      []string           `json:"definition_ids"`
	CreatedBy          string             `json:"created_by"`
	CreatedMS          int64              `json:"created_ms"`
	RequestSHA256      string             `json:"request_sha256"`
}

type ReleaseManifest struct {
	SchemaVersion string                 `json:"schema_version" required:"true"`
	ID            string                 `json:"id" required:"true"`
	Name          string                 `json:"name" required:"true"`
	Program       string                 `json:"program" required:"true" enum:"edge,cloud"`
	Components    []ReleaseComponent     `json:"components" minItems:"3" maxItems:"128" required:"true"`
	Template      *ReleaseTemplateOrigin `json:"template,omitempty"`
}

type Release struct {
	ID        string          `json:"id"`
	Manifest  ReleaseManifest `json:"manifest"`
	SHA256    string          `json:"sha256"`
	Order     []string        `json:"order"`
	CreatedMS int64           `json:"created_ms"`
	CreatedBy string          `json:"created_by"`
	Version   int64           `json:"version"`
}

type ReleaseArtifact struct {
	SHA256    string       `json:"sha256"`
	Size      int64        `json:"size"`
	Build     ProgramBuild `json:"build"`
	CreatedMS int64        `json:"created_ms"`
}

type ReleaseIssue struct {
	ComponentID string   `json:"component_id"`
	Code        string   `json:"code"`
	Message     string   `json:"message"`
	Recovery    []string `json:"recovery"`
}

type ReleaseValidation struct {
	Valid  bool           `json:"valid"`
	SHA256 string         `json:"sha256"`
	Order  []string       `json:"order"`
	Issues []ReleaseIssue `json:"issues"`
}

type ReleaseRuntimeComponent struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Version        string `json:"version"`
	SHA256         string `json:"sha256"`
	AppliedSHA256  string `json:"applied_sha256"`
	AppliedVersion int64  `json:"applied_version"`
}

type ReleaseRuntime struct {
	NodeID            string                    `json:"node_id"`
	Program           string                    `json:"program"`
	ProcessInstanceID string                    `json:"process_instance_id"`
	PID               int                       `json:"pid"`
	StartedMS         int64                     `json:"started_ms"`
	ProgramSHA256     string                    `json:"program_sha256"`
	Build             ProgramBuild              `json:"build"`
	ReleaseID         string                    `json:"release_id"`
	ReleaseSHA256     string                    `json:"release_sha256"`
	MigrationVersion  int64                     `json:"migration_version"`
	Components        []ReleaseRuntimeComponent `json:"components"`
	PolicySHA256      string                    `json:"policy_sha256"`
	Healthy           bool                      `json:"healthy"`
}

type ReleaseNodeReport struct {
	DeploymentID  string          `json:"deployment_id" required:"true"`
	IdentityID    string          `json:"identity_id" required:"true"`
	NodeID        string          `json:"node_id" required:"true"`
	Generation    int64           `json:"generation" minimum:"1" required:"true"`
	Sequence      int64           `json:"sequence" minimum:"1" required:"true"`
	ReleaseSHA256 string          `json:"release_sha256" required:"true"`
	State         string          `json:"state" enum:"prepared,applied,running,failed" required:"true"`
	ComponentID   string          `json:"component_id,omitempty"`
	Reason        string          `json:"reason,omitempty"`
	Runtime       *ReleaseRuntime `json:"runtime,omitempty"`
}

type ReleaseTarget struct {
	IdentityID         string          `json:"identity_id"`
	NodeID             string          `json:"node_id"`
	Program            string          `json:"program"`
	Batch              int             `json:"batch"`
	Generation         int64           `json:"generation"`
	State              string          `json:"state"`
	DesiredSHA256      string          `json:"desired_sha256"`
	PreparedSHA256     string          `json:"prepared_sha256"`
	AppliedSHA256      string          `json:"applied_sha256"`
	RunningSHA256      string          `json:"running_sha256"`
	AgentInstanceID    string          `json:"agent_instance_id"`
	AgentInstanceEpoch int64           `json:"agent_instance_epoch"`
	LastSequence       int64           `json:"last_sequence"`
	LastReportSHA256   string          `json:"last_report_sha256"`
	LastSeenMS         int64           `json:"last_seen_ms"`
	LastReportMS       int64           `json:"last_report_ms"`
	ReportAgeMS        int64           `json:"report_age_ms"`
	ReportFresh        bool            `json:"report_fresh"`
	FailureComponent   string          `json:"failure_component,omitempty"`
	Reason             string          `json:"reason,omitempty"`
	Runtime            *ReleaseRuntime `json:"runtime,omitempty"`
}

type ReleaseBatch struct {
	Index          int      `json:"index"`
	IdentityIDs    []string `json:"identity_ids"`
	State          string   `json:"state"`
	EnteredMS      int64    `json:"entered_ms"`
	CompletedMS    int64    `json:"completed_ms"`
	EntryCondition string   `json:"entry_condition"`
}

type ReleaseDeployment struct {
	ID             string           `json:"id"`
	ReleaseID      string           `json:"release_id"`
	ReleaseSHA256  string           `json:"release_sha256"`
	GroupID        string           `json:"group_id"`
	Version        int64            `json:"version"`
	State          string           `json:"state"`
	CurrentBatch   int              `json:"current_batch"`
	Batches        []ReleaseBatch   `json:"batches"`
	Targets        []ReleaseTarget  `json:"targets"`
	CreatedMS      int64            `json:"created_ms"`
	UpdatedMS      int64            `json:"updated_ms"`
	CreatedBy      string           `json:"created_by"`
	RequestSHA256  string           `json:"request_sha256"`
	RollbackOf     string           `json:"rollback_of,omitempty"`
	Reason         string           `json:"reason,omitempty"`
	AllowedActions []BusinessAction `json:"allowed_actions"`
}

type ReleaseAssignment struct {
	IdentityID   string `json:"identity_id"`
	DeploymentID string `json:"deployment_id"`
	Generation   int64  `json:"generation"`
	Version      int64  `json:"version"`
}

type ReleaseDesired struct {
	Available      bool                    `json:"available"`
	Configurations []ConfigurationEnvelope `json:"configurations"`
	DeploymentID   string                  `json:"deployment_id"`
	Generation     int64                   `json:"generation"`
	Action         string                  `json:"action"`
	Release        *Release                `json:"release,omitempty"`
	Target         *ReleaseTarget          `json:"target,omitempty"`
}
