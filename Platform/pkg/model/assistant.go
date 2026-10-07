package model

type AssistantMessage struct {
	Role    string `json:"role" enum:"user,assistant" required:"true"`
	Content string `json:"content,omitempty"`
}
type AssistantRequest struct {
	Messages []AssistantMessage `json:"messages" minItems:"1" maxItems:"100" required:"true"`
}

// AssistantEvent describes the JSON payload of one SSE data event.
type AssistantEvent struct {
	Type            string                  `json:"type" enum:"investigation,tool,evidence,delta,final,done,error" required:"true"`
	InvestigationID string                  `json:"investigation_id" required:"true"`
	URL             string                  `json:"url,omitempty"`
	Name            string                  `json:"name,omitempty"`
	ToolName        string                  `json:"tool_name,omitempty"`
	ToolCallID      string                  `json:"tool_call_id,omitempty"`
	EvidenceID      string                  `json:"evidence_id,omitempty"`
	Status          string                  `json:"status,omitempty"`
	Delivery        string                  `json:"delivery,omitempty"`
	VisibleSHA256   string                  `json:"visible_sha256,omitempty"`
	OriginalBytes   int                     `json:"original_bytes,omitempty"`
	VisibleBytes    int                     `json:"visible_bytes,omitempty"`
	BudgetBytes     int                     `json:"budget_bytes,omitempty"`
	Text            string                  `json:"text,omitempty"`
	Provisional     bool                    `json:"provisional,omitempty"`
	Answer          string                  `json:"answer,omitempty"`
	Error           string                  `json:"error,omitempty"`
	EvidenceStatus  string                  `json:"evidence_status,omitempty" enum:"pending,supported,partial,unsupported,missing"`
	References      []EvidenceReference     `json:"references,omitempty"`
	Evidence        []InvestigationEvidence `json:"evidence,omitempty"`
}
