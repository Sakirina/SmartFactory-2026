package model

const ExecutionPlanFormat = "smartfactory.rules/1"

// ExecutionPlan is immutable once published. ContentSHA256 excludes this plan
// from the definition; SHA256 covers the complete plan with its SHA256 cleared.
type ExecutionPlan struct {
	ID                string          `json:"id"`
	Format            string          `json:"format"`
	DefinitionID      string          `json:"definition_id"`
	DefinitionVersion int64           `json:"definition_version"`
	ContentSHA256     string          `json:"content_sha256"`
	SHA256            string          `json:"sha256"`
	Kind              string          `json:"kind"`
	Selector          Selector        `json:"selector"`
	Outputs           []Output        `json:"outputs"`
	Policy            Policy          `json:"policy"`
	Dependencies      []string        `json:"dependencies"`
	Nodes             []ExecutionNode `json:"nodes"`
}

type ExecutionNode struct {
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	Inputs      []Connection    `json:"inputs"`
	Params      NodeParameters  `json:"params"`
	Expression  *ExpressionTree `json:"expression,omitempty"`
	ErrorOutput bool            `json:"error_output"`
}

// NodeParameters is the typed parameter representation consumed by execution.
// Authoring documents retain their original parameter objects and extension keys.
type NodeParameters struct {
	Key        string `json:"key,omitempty"`
	Function   string `json:"function,omitempty"`
	Operator   string `json:"operator,omitempty"`
	Mode       string `json:"mode,omitempty"`
	Direction  string `json:"direction,omitempty"`
	Severity   string `json:"severity,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Value      any    `json:"value,omitempty"`
	High       any    `json:"high,omitempty"`
	Low        any    `json:"low,omitempty"`
}

// ExpressionTree is a bounded, serializable instruction tree. Runtime execution
// interprets these instructions and never invokes the expression parser.
type ExpressionTree struct {
	Op    string            `json:"op"`
	Name  string            `json:"name,omitempty"`
	Value any               `json:"value,omitempty"`
	Args  []*ExpressionTree `json:"args,omitempty"`
}

type AssetVersion struct {
	ID          string `json:"id"`
	Version     int64  `json:"version"`
	EffectiveMS int64  `json:"effective_ms"`
	ParentID    string `json:"parent_id"`
	SamplingMS  int64  `json:"sampling_ms"`
}
