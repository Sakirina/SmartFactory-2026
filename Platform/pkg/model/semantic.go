package model

type SemanticIdentity struct {
	ID                string `json:"id"`
	Version           int64  `json:"version"`
	DefinitionID      string `json:"definition_id"`
	DefinitionVersion int64  `json:"definition_version"`
	ContentHash       string `json:"content_hash"`
}

type SemanticChange struct {
	Category   string `json:"category"`
	Path       string `json:"path"`
	Change     string `json:"change"`
	Before     any    `json:"before,omitempty"`
	After      any    `json:"after,omitempty"`
	BeforeType string `json:"before_type"`
	AfterType  string `json:"after_type"`
	Unit       string `json:"unit,omitempty"`
}

type SemanticDifference struct {
	Draft      SemanticIdentity `json:"draft"`
	Published  SemanticIdentity `json:"published"`
	Changes    []SemanticChange `json:"changes"`
	Equivalent bool             `json:"equivalent"`
}

type ImpactReference struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Version  int64  `json:"version"`
	Via      string `json:"via"`
	Location string `json:"location"`
}

type ImpactIssue struct {
	Code        string `json:"code"`
	ID          string `json:"id,omitempty"`
	Location    string `json:"location"`
	Description string `json:"description"`
}

type ImpactAnalysis struct {
	Draft      SemanticIdentity  `json:"draft"`
	References []ImpactReference `json:"references"`
	Issues     []ImpactIssue     `json:"issues"`
	Visited    int               `json:"visited"`
	Budget     int               `json:"budget"`
	Complete   bool              `json:"complete"`
}
