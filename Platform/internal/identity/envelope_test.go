package identity

import (
	"bytes"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"testing"
	"time"
)

func TestPermissionsDeliverCredentialsOnlyToRegisteredEdge(t *testing.T) {
	ctx := context.Background()
	cloud := manager(t)
	edge := manager(t)
	edge.Master = bytes.Repeat([]byte{7}, 32)
	edge.Edge = true
	secret := "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"
	_, e := cloud.CreateUser(ctx, model.Actor{}, model.User{ID: "engineer", Name: "Engineer", Login: "engineer", Active: true, Roles: []string{"engineer"}, Resources: []string{"factory"}}, "simulated-password", secret, 0)
	if e != nil {
		t.Fatal(e)
	}
	pub, e := edge.EncryptionPublicKey()
	if e != nil {
		t.Fatal(e)
	}
	b, e := cloud.SignBundleForNode(ctx, "edge-a", 1, pub)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(b)
	if bytes.Contains(raw, []byte(secret)) || bytes.Contains(raw, []byte("$2a$")) {
		t.Fatal("credential exposed in signed bundle")
	}
	if e = edge.ApplyBundle(ctx, b, cloud.Store.SignKey.Public().(ed25519.PublicKey)); e != nil {
		t.Fatal(e)
	}
	code, _ := TOTP(secret, time.Now())
	if _, _, e = edge.Login(ctx, "engineer", "simulated-password", code, true, "local"); e != nil {
		t.Fatal(e)
	}
	d, _ := edge.Store.Get(ctx, "credential", "engineer")
	c, _ := store.Decode[Credential](d)
	if _, e = cloud.Decrypt("engineer", c.TOTP); e == nil {
		t.Fatal("edge credential reused cloud encryption key")
	}
	other := manager(t)
	other.Master = bytes.Repeat([]byte{8}, 32)
	if e = other.ApplyBundle(ctx, b, cloud.Store.SignKey.Public().(ed25519.PublicKey)); e == nil {
		t.Fatal("wrong edge key opened envelope")
	}
	b.Credentials.Ciphertext = "AAAA"
	if e = edge.ApplyBundle(ctx, b, cloud.Store.SignKey.Public().(ed25519.PublicKey)); e == nil {
		t.Fatal("tampered ciphertext accepted")
	}
}
