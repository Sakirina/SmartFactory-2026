package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"competition2026/product/platform/pkg/model"
)

func TestValidationErrorsExcludeRequestValues(t *testing.T) {
	s, fixture := businessServer(t)
	ctx := context.Background()
	password := "validation-http-admin-password"
	if _, err := fixture.Identity.CreateUser(ctx, model.Actor{}, model.User{ID: "validation-admin", Login: "validation-admin", Name: "validation admin", Active: true, Roles: []string{"admin"}, Resources: []string{"*"}}, password, "", 0); err != nil {
		t.Fatal(err)
	}
	token, _, err := fixture.Identity.Login(ctx, "validation-admin", password, "", false, "validation-errors")
	if err != nil {
		t.Fatal(err)
	}
	created := call(s, token, "POST", "/api/sf/v1/workload-identities", map[string]any{
		"identity":         model.WorkloadIdentity{ID: "validation-workload", NodeID: "edge-a", Program: "edge", Purpose: "release-runtime", Enabled: true, Capabilities: []string{"release"}},
		"expected_version": 0,
	})
	if created.Code != 200 {
		t.Fatal(created.Code, created.Body.String())
	}
	var before model.WorkloadIdentity
	if err := json.Unmarshal(created.Body.Bytes(), &before); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		name, marker, location, explanation string
		body                                map[string]any
	}{
		{"missing_parent_property", "synthetic-secret-parent-validation-probe", "body", "expected_version", map[string]any{"credential": "synthetic-secret-parent-validation-probe"}},
		{"credential_too_short", "private-short", "body.credential", "length", map[string]any{"credential": "private-short", "expected_version": before.Version}},
		{"credential_too_long", strings.Repeat("synthetic-private-", 40), "body.credential", "length", map[string]any{"credential": strings.Repeat("synthetic-private-", 40), "expected_version": before.Version}},
		{"credential_wrong_type", "synthetic-nested-secret", "body.credential", "string", map[string]any{"credential": map[string]any{"nested": "synthetic-nested-secret"}, "expected_version": before.Version}},
	} {
		t.Run(item.name, func(t *testing.T) {
			response := call(s, token, "POST", "/api/sf/v1/workload-identities/validation-workload/rotate", item.body)
			if response.Code != 422 {
				t.Fatalf("validation status: %d", response.Code)
			}
			if strings.Contains(response.Body.String(), item.marker) {
				t.Error("validation response echoed a synthetic private request value")
			}
			var body struct {
				Error   string           `json:"error"`
				Details []map[string]any `json:"details"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, detail := range body.Details {
				if _, ok := detail["value"]; ok {
					t.Error("validation response retained request value data")
				}
				message, _ := detail["message"].(string)
				if detail["location"] == item.location && strings.Contains(message, item.explanation) {
					found = true
				}
			}
			if body.Error == "" || !found {
				t.Fatal("validation response lost its field location or constraint explanation")
			}
			after, err := s.NodeIdentities().Lookup(ctx, before.ID)
			if err != nil || after.Version != before.Version || after.Generation != before.Generation {
				t.Fatal("invalid credential request changed workload metadata", err)
			}
		})
	}
}
