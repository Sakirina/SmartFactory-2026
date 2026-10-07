package releaseruntime

import (
	"encoding/json"
	"strings"
	"testing"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/pkg/model"
)

func TestPrivateReleaseCacheAuthenticatedToNodeAndRelease(t *testing.T) {
	cipher := &identity.Manager{Master: make([]byte, 32)}
	secret := "private-token-do-not-persist-as-json"
	staged := Staged{NodeID: "edge-a", Release: model.Release{SHA256: strings.Repeat("a", 64)}, Private: map[string]json.RawMessage{"credential": json.RawMessage(`"` + secret + `"`)}}
	cached, err := Seal(staged, cipher)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(cached)
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "credential") {
		t.Fatal("private data exposed by encrypted cache")
	}
	actual, err := Unseal(cached, "edge-a", cipher)
	if err != nil || string(actual.Private["credential"]) != `"`+secret+`"` {
		t.Fatal(actual, err)
	}
	if _, err = Unseal(cached, "edge-b", cipher); err == nil {
		t.Fatal("cross-node cache accepted")
	}
	changed := cached
	changed.ReleaseSHA256 = strings.Repeat("b", 64)
	if _, err = Unseal(changed, "edge-a", cipher); err == nil {
		t.Fatal("changed release accepted")
	}
	key := make([]byte, 32)
	key[0] = 1
	if _, err = Unseal(cached, "edge-a", &identity.Manager{Master: key}); err == nil {
		t.Fatal("wrong master key accepted")
	}
}
