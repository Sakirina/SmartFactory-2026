package identity

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func manager(t *testing.T) *Manager {
	t.Helper()
	s, e := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), "edge-a", make([]byte, 32))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return &Manager{Store: s, Master: make([]byte, 32)}
}
func TestAuthenticationRevocationAIAndTOTP(t *testing.T) {
	m := manager(t)
	ctx := context.Background()
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	now := time.Unix(59, 0)
	code, e := TOTP(secret, now)
	if e != nil || code != "287082" {
		t.Fatalf("RFC6238 fixture: %s %v", code, e)
	}
	if !CheckTOTP(secret, "287082", now) || CheckTOTP(secret, "000000", now) {
		t.Fatal("TOTP verification failed")
	}
	u, e := m.CreateUser(ctx, model.Actor{}, model.User{ID: "person", Login: "person", Name: "Person", Roles: []string{"admin"}, Resources: []string{"*"}, Active: true}, "testing-password", secret, 0)
	if e != nil {
		t.Fatal(e)
	}
	token, p, e := m.Login(ctx, "person", "testing-password", "", true, "local")
	if e != nil {
		t.Fatal(e)
	}
	if e = m.Permit(ctx, p, "publish", "area-a"); e != nil {
		t.Fatal(e)
	}
	u.Active = false
	if _, e = m.CreateUser(ctx, p.Actor, u, "", "", 1); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Authenticate(ctx, token); e == nil {
		t.Fatal("revoked identity retained session access")
	}
	p.User.Active = true
	p.User.AI = true
	for _, action := range []string{"publish", "control", "secret_read", "identity", "approve"} {
		if e = m.Permit(ctx, p, action, "area-a"); e == nil {
			t.Fatalf("AI allowed %s", action)
		}
	}
	if e = m.Permit(ctx, p, "draft", "area-a"); e != nil {
		t.Fatal(e)
	}
}
func TestBundleSignatureExpiryAndCredentialEncryption(t *testing.T) {
	m := manager(t)
	ctx := context.Background()
	cipher, e := m.Encrypt("key-a", "secret-value")
	if e != nil {
		t.Fatal(e)
	}
	if value, e := m.Decrypt("key-a", cipher); e != nil || value != "secret-value" {
		t.Fatal(value, e)
	}
	if _, e = m.Decrypt("key-b", cipher); e == nil {
		t.Fatal("credential identifier was not authenticated")
	}
	b, e := m.SignBundle(ctx, "edge-a", 1)
	if e != nil {
		t.Fatal(e)
	}
	pub := m.Store.SignKey.Public().(ed25519.PublicKey)
	m.Edge = true
	if e = m.ApplyBundle(ctx, b, pub); e != nil {
		t.Fatal(e)
	}
	if e = m.offlineCheck(ctx); e != nil {
		t.Fatal(e)
	}
	b.NodeID = "unregistered"
	if e = m.ApplyBundle(ctx, b, pub); e == nil {
		t.Fatal("altered bundle accepted")
	}
	future := m.Store.Now().Add(49 * time.Hour)
	m.Store.Now = func() time.Time { return future }
	if e = m.offlineCheck(ctx); e == nil {
		t.Fatal("expired offline permission allowed")
	}
}
func TestNewBundleRevokesAbsentUsersAndTeamGrants(t *testing.T) {
	m := manager(t)
	ctx := context.Background()
	_, e := m.CreateUser(ctx, model.Actor{}, model.User{ID: "removed", Name: "Removed", Login: "removed", Active: true, Roles: []string{"viewer"}, Teams: []string{"team"}, Resources: []string{"area"}}, "testing-password", "", 0)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Store.Put(ctx, "grant", "old", 0, Grant{ID: "old", TeamID: "team", GroupID: "foreign", Actions: []string{"read"}}); e != nil {
		t.Fatal(e)
	}
	b, e := m.SignBundle(ctx, "edge-a", 1)
	if e != nil {
		t.Fatal(e)
	}
	pub := m.Store.SignKey.Public().(ed25519.PublicKey)
	if e = m.ApplyBundle(ctx, b, pub); e != nil {
		t.Fatal(e)
	}
	b.Version = 2
	b.Users = []model.User{}
	b.Grants = []Grant{}
	b.Signature = ""
	raw, _ := json.Marshal(b)
	b.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(m.Store.SignKey, raw))
	if e = m.ApplyBundle(ctx, b, pub); e != nil {
		t.Fatal(e)
	}
	doc, e := m.Store.Get(ctx, "user", "removed")
	if e != nil {
		t.Fatal(e)
	}
	u, e := store.Decode[model.User](doc)
	if e != nil || u.Active {
		t.Fatal("absent user remains active", e)
	}
	grants, e := m.Store.List(ctx, "grant")
	if e != nil || len(grants) != 0 {
		t.Fatal("obsolete grants remain", e)
	}
}
