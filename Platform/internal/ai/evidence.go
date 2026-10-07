package ai

import (
	"encoding/json"
	"regexp"
	"strings"

	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

var bearerEvidence = regexp.MustCompile(`(?i)bearer\s+[a-z0-9._~+/-]+`)
var keyEvidence = regexp.MustCompile(`sk-[A-Za-z0-9_-]{12,}`)

func evidenceText(value, key string) string {
	if key != "" {
		value = strings.ReplaceAll(value, key, "[redacted]")
	}
	value = bearerEvidence.ReplaceAllString(value, "Bearer [redacted]")
	return keyEvidence.ReplaceAllString(value, "[redacted]")
}

func evidenceValue(value any, key string) any {
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for field, part := range v {
			clean := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(field))
			switch clean {
			case "token", "apikey", "password", "credentials", "credential", "authorization", "authheader", "clientsecret", "secret", "privatekey", "accesstoken", "refreshtoken", "bearertoken", "cookie", "setcookie", "encryptedvalue", "ciphertext", "totp":
				out[field] = "[redacted]"
			default:
				out[field] = evidenceValue(part, key)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, part := range v {
			out[i] = evidenceValue(part, key)
		}
		return out
	case string:
		return evidenceText(v, key)
	default:
		return value
	}
}

func prepareEvidence(app *application.Investigations, investigation model.Investigation, call ToolCall, args map[string]any, value any, toolErr error, key string) (model.InvestigationEvidence, error) {
	e := model.InvestigationEvidence{ID: identity.ID(), InvestigationID: investigation.ID, ToolCallID: call.ID, ToolName: call.Function.Name, Status: "ok", CreatedMS: app.Store.CurrentTime().UnixMilli(), ExpiresMS: investigation.ExpiresMS, Resources: []model.EvidenceResource{}}
	if toolErr != nil {
		e.Status = "error"
		value = map[string]string{"error": evidenceText(toolErr.Error(), key)}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return e, err
	}
	var wire any
	if err = store.DecodeJSON(raw, &wire); err != nil {
		return e, err
	}
	wire = evidenceValue(wire, key)
	raw, err = json.Marshal(wire)
	if err != nil {
		return e, err
	}
	e.OriginalBytes = len(raw)
	argRaw, err := json.Marshal(args)
	if err != nil {
		return e, err
	}
	var argValue any
	if err = store.DecodeJSON(argRaw, &argValue); err != nil {
		return e, err
	}
	e.Arguments, err = json.Marshal(evidenceValue(argValue, key))
	if err != nil {
		return e, err
	}
	if wire == nil {
		e.Status = "empty"
	}
	if obj, ok := wire.(map[string]any); ok {
		for _, field := range []string{"items", "points"} {
			if items, ok := obj[field].([]any); ok && len(items) == 0 && toolErr == nil {
				e.Status = "empty"
			}
		}
	}
	meta := map[string]any{"id": e.ID, "investigation_id": e.InvestigationID, "tool_call_id": call.ID, "status": e.Status, "budget_bytes": application.AIResultBudget, "original_bytes": e.OriginalBytes, "citation": "[evidence:" + e.ID + "]"}
	envelope := func(result any) any {
		if fields, ok := result.(map[string]any); ok {
			out := map[string]any{}
			for k, v := range fields {
				out[k] = v
			}
			out["_evidence"] = meta
			return out
		}
		return map[string]any{"result": result, "_evidence": meta}
	}
	visible, err := json.Marshal(envelope(wire))
	if err != nil {
		return e, err
	}
	if len(visible) > application.AIResultBudget {
		e.Status = "budget_exceeded"
		meta["status"] = e.Status
		wire = map[string]any{"error": "tool result exceeds model context budget; query a smaller scope", "budget_exceeded": true}
		visible, err = json.Marshal(envelope(wire))
		if err != nil {
			return e, err
		}
	} else if toolErr == nil {
		e.Resources = app.Resources(call.Function.Name, args, wire)
	}
	e.VisibleContent = string(visible)
	return e, nil
}
