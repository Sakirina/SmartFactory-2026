package identity

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
	"golang.org/x/crypto/bcrypt"
)

var ErrDenied = errors.New("permission denied")
var ErrAuthentication = errors.New("invalid or expired credentials")

type Manager struct {
	Store  *store.Store
	Master []byte
	Edge   bool
}
type Principal struct {
	User          model.User
	Actor         model.Actor
	SessionID     string
	Local         bool
	StepUpUntilMS int64
}
type Session struct {
	ID            string `json:"id"`
	UserID        string `json:"user_id"`
	ExpiresMS     int64  `json:"expires_ms"`
	StepUpUntilMS int64  `json:"step_up_until_ms"`
	Local         bool   `json:"local"`
	Source        string `json:"source"`
	DelegatedAI   bool   `json:"delegated_ai"`
	AIReadOnly    bool   `json:"ai_read_only"`
}
type Credential struct {
	Hash string `json:"hash"`
	TOTP string `json:"totp"`
}
type Grant struct {
	ID        string   `json:"id"`
	TeamID    string   `json:"team_id"`
	GroupID   string   `json:"group_id"`
	Resources []string `json:"resources"`
	Actions   []string `json:"actions"`
}
type PermissionBundle struct {
	NodeID      string              `json:"node_id"`
	IssuedMS    int64               `json:"issued_ms"`
	ExpiresMS   int64               `json:"expires_ms"`
	Version     int64               `json:"version"`
	Users       []model.User        `json:"users"`
	Grants      []Grant             `json:"grants"`
	Credentials *CredentialEnvelope `json:"credentials,omitempty"`
	Signature   string              `json:"signature"`
}

func ID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func tokenHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
func (m *Manager) Encrypt(id, value string) (string, error) {
	key := sha256.Sum256(append(append([]byte{}, m.Master...), []byte("credentials")...))
	block, e := aes.NewCipher(key[:])
	if e != nil {
		return "", e
	}
	a, e := cipher.NewGCM(block)
	if e != nil {
		return "", e
	}
	nonce := make([]byte, a.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return "", e
	}
	data := a.Seal(nonce, nonce, []byte(value), []byte(id))
	return base64.StdEncoding.EncodeToString(data), nil
}
func (m *Manager) Decrypt(id, encrypted string) (string, error) {
	key := sha256.Sum256(append(append([]byte{}, m.Master...), []byte("credentials")...))
	block, e := aes.NewCipher(key[:])
	if e != nil {
		return "", e
	}
	a, e := cipher.NewGCM(block)
	if e != nil {
		return "", e
	}
	b, e := base64.StdEncoding.DecodeString(encrypted)
	if e != nil || len(b) < a.NonceSize() {
		return "", errors.New("invalid encrypted value")
	}
	p, e := a.Open(nil, b[:a.NonceSize()], b[a.NonceSize():], []byte(id))
	return string(p), e
}
func (m *Manager) CreateUser(ctx context.Context, actor model.Actor, u model.User, password, totp string, expected int64) (model.User, error) {
	if u.ID == "" {
		u.ID = ID()
	}
	if u.Login == "" || u.Name == "" {
		return u, errors.New("name and login are required")
	}
	if password != "" && len(password) < 12 {
		return u, errors.New("password must contain at least 12 characters")
	}
	if u.AI {
		u.Roles = []string{"ai"}
	}
	u.Version = expected + 1
	users, e := m.Store.List(ctx, "user")
	if e != nil {
		return u, e
	}
	for _, d := range users {
		other, e := store.Decode[model.User](d)
		if e != nil {
			return u, e
		}
		if other.ID != u.ID && strings.EqualFold(other.Login, u.Login) {
			return u, store.ErrConflict
		}
	}
	var cred *Credential
	if password != "" {
		h, e := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if e != nil {
			return u, e
		}
		enc, e := m.Encrypt(u.ID, totp)
		if e != nil {
			return u, e
		}
		cred = &Credential{Hash: string(h), TOTP: enc}
	}
	e = m.Store.Write(ctx, func(t *store.Tx) error {
		if _, e := t.Put("user", u.ID, expected, u); e != nil {
			return e
		}
		if cred != nil {
			if _, e := t.Put("credential", u.ID, -1, cred); e != nil {
				return e
			}
		}
		return t.Audit(actor, "identity.user.update", u.ID, "", u)
	})
	return u, e
}
func (m *Manager) Login(ctx context.Context, login, password, code string, local bool, source string) (string, Principal, error) {
	users, e := m.Store.List(ctx, "user")
	if e != nil {
		return "", Principal{}, e
	}
	var found model.User
	for _, d := range users {
		u, e := store.Decode[model.User](d)
		if e != nil {
			return "", Principal{}, e
		}
		if strings.EqualFold(u.Login, login) {
			found = u
			break
		}
	}
	if found.ID == "" || !found.Active {
		return "", Principal{}, ErrAuthentication
	}
	d, e := m.Store.Get(ctx, "credential", found.ID)
	if e != nil {
		return "", Principal{}, ErrAuthentication
	}
	cred, e := store.Decode[Credential](d)
	if e != nil {
		return "", Principal{}, e
	}
	if bcrypt.CompareHashAndPassword([]byte(cred.Hash), []byte(password)) != nil {
		return "", Principal{}, ErrAuthentication
	}
	if e = m.offlineCheck(ctx); e != nil {
		return "", Principal{}, e
	}
	s := Session{ID: ID(), UserID: found.ID, ExpiresMS: m.Store.Now().Add(12 * time.Hour).UnixMilli(), Local: local, Source: source}
	if code != "" {
		secret, e := m.Decrypt(found.ID, cred.TOTP)
		if e != nil || secret == "" || !CheckTOTP(secret, code, m.Store.Now()) {
			return "", Principal{}, ErrAuthentication
		}
		s.StepUpUntilMS = m.Store.Now().Add(5 * time.Minute).UnixMilli()
	}
	token := ID() + ID()
	e = m.Store.Write(ctx, func(t *store.Tx) error {
		if e := t.SetEphemeral("session", tokenHash(token), s); e != nil {
			return e
		}
		return t.Audit(model.Actor{UserID: found.ID, Name: found.Name, Source: source, SessionID: s.ID}, "identity.login", found.ID, s.ID, map[string]any{"local": local, "step_up": s.StepUpUntilMS > 0})
	})
	if e != nil {
		return "", Principal{}, e
	}
	p, e := m.Authenticate(ctx, token)
	return token, p, e
}
func (m *Manager) Authenticate(ctx context.Context, token string) (Principal, error) {
	p := Principal{}
	if token == "" {
		return p, ErrAuthentication
	}
	d, e := m.Store.Get(ctx, "session", tokenHash(token))
	if e != nil {
		return p, ErrAuthentication
	}
	s, e := store.Decode[Session](d)
	if e != nil || s.ExpiresMS <= m.Store.Now().UnixMilli() {
		return p, ErrAuthentication
	}
	if e = m.offlineCheck(ctx); e != nil {
		return p, e
	}
	d, e = m.Store.Get(ctx, "user", s.UserID)
	if e != nil {
		return p, ErrAuthentication
	}
	u, e := store.Decode[model.User](d)
	if e != nil || !u.Active {
		return p, ErrAuthentication
	}
	if s.DelegatedAI {
		u.AI = true
		u.Roles = []string{"ai"}
		if s.AIReadOnly {
			u.Roles = []string{"viewer"}
		}
	}
	p.User = u
	p.SessionID = s.ID
	p.Local = s.Local
	p.StepUpUntilMS = s.StepUpUntilMS
	p.Actor = model.Actor{UserID: u.ID, Name: u.Name, DepartmentID: u.DepartmentID, Roles: u.Roles, Source: s.Source, SessionID: s.ID, AI: u.AI}
	return p, nil
}

func (m *Manager) DelegateAI(ctx context.Context, p Principal) (string, error) {
	if e := m.Permit(ctx, p, "read", ""); e != nil {
		return "", e
	}
	session := Session{ID: ID(), UserID: p.User.ID, ExpiresMS: m.Store.Now().Add(15 * time.Minute).UnixMilli(), Source: "assistant", DelegatedAI: true, AIReadOnly: m.Permit(ctx, p, "draft", "") != nil}
	token := ID() + ID()
	e := m.Store.Write(ctx, func(t *store.Tx) error { return t.SetEphemeral("session", tokenHash(token), session) })
	return token, e
}
func (m *Manager) Logout(ctx context.Context, token string) error {
	_, e := m.Store.DB.ExecContext(ctx, "DELETE FROM documents WHERE kind='session' AND id=$1", tokenHash(token))
	return e
}
func (m *Manager) Permit(ctx context.Context, p Principal, action, resource string) error {
	if !p.User.Active {
		return ErrDenied
	}
	if p.User.AI && action != "read" && action != "draft" {
		return ErrDenied
	}
	allowed := false
	for _, role := range p.User.Roles {
		switch role {
		case "admin":
			allowed = true
		case "viewer":
			allowed = allowed || action == "read"
		case "engineer":
			allowed = allowed || action == "read" || action == "draft" || action == "publish" || action == "control" || action == "approve" || action == "register" || action == "dashboard"
		case "leader", "safety":
			allowed = allowed || action == "read" || action == "approve"
		case "ai":
			allowed = allowed || action == "read" || action == "draft"
		case "gateway":
			allowed = allowed || action == "ingest"
		}
	}
	resources := append([]string{}, p.User.Resources...)
	if allowed && (resource == "" || contains(resources, "*") || contains(resources, resource)) {
		return nil
	}
	grants, e := m.Store.List(ctx, "grant")
	if e != nil {
		return e
	}
	for _, d := range grants {
		g, e := store.Decode[Grant](d)
		if e != nil {
			return e
		}
		if contains(p.User.Teams, g.TeamID) && contains(g.Actions, action) {
			allowed = true
			resources = append(resources, g.Resources...)
			resources = append(resources, g.GroupID)
		}
	}
	if !allowed {
		return ErrDenied
	}
	if resource == "" || contains(resources, "*") || contains(resources, resource) {
		return nil
	}
	// Grants to a logical asset include its descendants, bounded against cycles.
	for id, i := resource, 0; id != "" && i < 64; i++ {
		d, e := m.Store.Get(ctx, "entity", id)
		if e != nil {
			break
		}
		v, e := store.Decode[model.Entity](d)
		if e != nil {
			return e
		}
		id = v.ParentID
		if contains(resources, id) {
			return nil
		}
	}
	return ErrDenied
}
func (m *Manager) offlineCheck(ctx context.Context) error {
	if !m.Edge {
		return nil
	}
	d, e := m.Store.Get(ctx, "permission_bundle", "active")
	if e != nil {
		return ErrAuthentication
	}
	b, e := store.Decode[PermissionBundle](d)
	if e != nil || b.NodeID != m.Store.NodeID || b.ExpiresMS <= m.Store.Now().UnixMilli() {
		return ErrAuthentication
	}
	return nil
}
func (m *Manager) SignBundle(ctx context.Context, nodeID string, version int64) (PermissionBundle, error) {
	b := PermissionBundle{NodeID: nodeID, IssuedMS: m.Store.Now().UnixMilli(), ExpiresMS: m.Store.Now().Add(store.Milliseconds(m.Store.Policy().PermissionTTLMS)).UnixMilli(), Version: version, Users: []model.User{}, Grants: []Grant{}}
	ds, e := m.Store.List(ctx, "user")
	if e != nil {
		return b, e
	}
	for _, d := range ds {
		u, e := store.Decode[model.User](d)
		if e != nil {
			return b, e
		}
		b.Users = append(b.Users, u)
	}
	ds, e = m.Store.List(ctx, "grant")
	if e != nil {
		return b, e
	}
	for _, d := range ds {
		g, e := store.Decode[Grant](d)
		if e != nil {
			return b, e
		}
		b.Grants = append(b.Grants, g)
	}
	raw, _ := json.Marshal(b)
	b.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(m.Store.SignKey, raw))
	return b, nil
}
func (m *Manager) ApplyBundle(ctx context.Context, b PermissionBundle, pub ed25519.PublicKey) error {
	sig, e := base64.StdEncoding.DecodeString(b.Signature)
	if e != nil {
		return e
	}
	signature := b.Signature
	b.Signature = ""
	raw, _ := json.Marshal(b)
	b.Signature = signature
	if len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, raw, sig) || b.NodeID != m.Store.NodeID || b.ExpiresMS <= m.Store.Now().UnixMilli() || b.IssuedMS > m.Store.Now().Add(time.Minute).UnixMilli() {
		return ErrDenied
	}
	credentials, e := m.openCredentials(b)
	if e != nil {
		return e
	}
	return m.Store.Write(ctx, func(t *store.Tx) error {
		if d, e := t.Get("permission_bundle", "active"); e == nil {
			old, e := store.Decode[PermissionBundle](d)
			if e != nil {
				return e
			}
			if b.Version <= old.Version {
				if store.Hash(b) == store.Hash(old) {
					return nil
				}
				return store.ErrConflict
			}
		}
		allowedUsers := map[string]bool{}
		for _, u := range b.Users {
			allowedUsers[u.ID] = true
		}
		rows, e := t.QueryContext(ctx, "SELECT kind,id,version,updated_ms,data FROM documents WHERE kind='user'")
		if e != nil {
			return e
		}
		removed := []model.User{}
		for rows.Next() {
			var kind, id, raw string
			var version, updated int64
			if e = rows.Scan(&kind, &id, &version, &updated, &raw); e != nil {
				rows.Close()
				return e
			}
			if !allowedUsers[id] {
				var u model.User
				if e = store.DecodeJSON([]byte(raw), &u); e != nil {
					rows.Close()
					return e
				}
				u.Active = false
				removed = append(removed, u)
			}
		}
		for id, credential := range credentials {
			if !allowedUsers[id] {
				return ErrDenied
			}
			if _, e := t.Put("credential", id, -1, credential); e != nil {
				return e
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, u := range removed {
			if _, e := t.Put("user", u.ID, -1, u); e != nil {
				return e
			}
		}
		if _, e := t.ExecContext(ctx, "DELETE FROM documents WHERE kind='grant'"); e != nil {
			return e
		}
		for _, u := range b.Users {
			if _, e := t.Put("user", u.ID, -1, u); e != nil {
				return e
			}
		}
		for _, g := range b.Grants {
			// Grant history is retained separately; the active authorization snapshot is replaced.
			if e := t.SetEphemeral("grant", g.ID, g); e != nil {
				return e
			}
		}
		_, e = t.Put("permission_bundle", "active", -1, b)
		return e
	})
}
func CheckTOTP(secret, code string, now time.Time) bool {
	if len(code) != 6 {
		return false
	}
	for offset := -1; offset <= 1; offset++ {
		expected, e := TOTP(secret, now.Add(time.Duration(offset)*30*time.Second))
		if e == nil && subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			return true
		}
	}
	return false
}
func TOTP(secret string, now time.Time) (string, error) {
	key, e := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if e != nil || len(key) < 16 {
		return "", errors.New("invalid TOTP secret")
	}
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(now.Unix()/30))
	h := hmac.New(sha1.New, key)
	h.Write(buf)
	sum := h.Sum(nil)
	offset := sum[len(sum)-1] & 15
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06s", strconv.Itoa(int(value%1000000))), nil
}
