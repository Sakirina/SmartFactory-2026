package model

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ResourceID accepts identities that can be carried through an escaped HTTP
// path segment, including Chinese, slashes and colons.
func ResourceID(id string) bool {
	if id == "" || id == "." || id == ".." || !utf8.ValidString(id) || utf8.RuneCountInString(id) > 200 {
		return false
	}
	return !strings.ContainsFunc(id, unicode.IsControl)
}

type InvestigationMessage struct {
	Role    string `json:"role" required:"true"`
	Content string `json:"content" required:"true"`
}

type EvidenceResource struct {
	Kind     string `json:"kind" required:"true"`
	ID       string `json:"id" required:"true"`
	Version  int64  `json:"version" required:"true"`
	Revision string `json:"revision,omitempty"`
	URL      string `json:"url,omitempty"`
}

type InvestigationEvidence struct {
	ID              string          `json:"id" required:"true"`
	InvestigationID string          `json:"investigation_id" required:"true"`
	Ordinal         int             `json:"ordinal" required:"true"`
	ToolCallID      string          `json:"tool_call_id" required:"true"`
	ToolName        string          `json:"tool_name" required:"true"`
	Arguments       json.RawMessage `json:"arguments" required:"true"`
	Status          string          `json:"status" enum:"ok,error,budget_exceeded,empty" required:"true"`
	Delivery        string          `json:"delivery" enum:"prepared,delivered" required:"true"`
	AccessStatus    string          `json:"access_status,omitempty"`
	CreatedMS       int64           `json:"created_ms" required:"true"`
	ExpiresMS       int64           `json:"expires_ms" required:"true"`
	OriginalBytes   int             `json:"original_bytes" required:"true"`
	VisibleBytes    int             `json:"visible_bytes" required:"true"`
	BudgetBytes     int             `json:"budget_bytes" required:"true"`
	VisibleSHA256   string          `json:"visible_sha256" required:"true"`
	// VisibleContent is the exact JSON string supplied as a tool result to the
	// model; details return it only after current access checks.
	VisibleContent string             `json:"visible_content,omitempty"`
	Resources      []EvidenceResource `json:"resources" required:"true"`
}

type EvidenceReference struct {
	ID     string `json:"id" required:"true"`
	Status string `json:"status" enum:"valid,unknown,cross_investigation,expired,deleted,forbidden,tool_error,budget_exceeded,empty,not_delivered" required:"true"`
	URL    string `json:"url,omitempty"`
}

type Investigation struct {
	ID             string                  `json:"id" required:"true"`
	UserID         string                  `json:"user_id" required:"true"`
	Version        int64                   `json:"version" required:"true"`
	Provider       string                  `json:"provider" required:"true"`
	API            string                  `json:"api" required:"true"`
	Model          string                  `json:"model" required:"true"`
	Messages       []InvestigationMessage  `json:"messages" required:"true"`
	Status         string                  `json:"status" enum:"running,completed,failed,cancelled,deleted" required:"true"`
	CreatedMS      int64                   `json:"created_ms" required:"true"`
	UpdatedMS      int64                   `json:"updated_ms" required:"true"`
	ExpiresMS      int64                   `json:"expires_ms" required:"true"`
	Answer         string                  `json:"answer,omitempty"`
	Error          string                  `json:"error,omitempty"`
	EvidenceStatus string                  `json:"evidence_status" enum:"pending,supported,partial,unsupported,missing" required:"true"`
	References     []EvidenceReference     `json:"references" required:"true"`
	Evidence       []InvestigationEvidence `json:"evidence" required:"true"`
}

type InvestigationList struct {
	Items     []Investigation `json:"items" required:"true"`
	HasMore   bool            `json:"has_more" required:"true"`
	NextAfter string          `json:"next_after,omitempty"`
}

type AIDocumentScope struct {
	Kind  string `json:"kind,omitempty"`
	After string `json:"after,omitempty"`
	Limit int    `json:"limit" required:"true"`
}

type AIDocumentPage struct {
	Items           []json.RawMessage `json:"items" required:"true"`
	Scope           AIDocumentScope   `json:"scope" required:"true"`
	HasMore         bool              `json:"has_more" required:"true"`
	NextAfter       string            `json:"next_after,omitempty"`
	ScanBudget      int               `json:"scan_budget" required:"true"`
	BudgetExhausted bool              `json:"budget_exhausted" required:"true"`
}
