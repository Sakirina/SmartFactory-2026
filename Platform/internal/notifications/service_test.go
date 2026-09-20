package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type settings map[string]Channel

func (s settings) Value(_ context.Context, key string) (any, error) {
	v, ok := s[key]
	if !ok {
		return nil, errors.New("missing setting")
	}
	return v, nil
}
func fixture(t *testing.T) *Service {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "notification.db"), "cloud", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &Service{Store: s, Identity: &identity.Manager{Store: s, Master: make([]byte, 32)}}
}
func user(t *testing.T, s *Service, id, department, resource string, active, ai bool) {
	t.Helper()
	u := model.User{ID: id, Name: id, Login: id, DepartmentID: department, Resources: []string{resource}, Roles: []string{"viewer"}, Active: active, AI: ai, Email: id + "@example.test", Phone: "test:" + id}
	if _, err := s.Store.Put(context.Background(), "user", id, 0, u); err != nil {
		t.Fatal(err)
	}
}
func message(id, channel string, recipients ...string) []byte {
	b, _ := json.Marshal(Message{ID: id, Channel: channel, Recipients: recipients, Alarm: model.Alarm{ID: "alarm-a", DefinitionID: "gas", EntityID: "gas-1", Severity: "CRITICAL", Active: true, StartedMS: 123}})
	return b
}
func status(t *testing.T, s *Service, id, want string) {
	t.Helper()
	d, err := s.Store.Get(context.Background(), "notification", id)
	if err != nil {
		t.Fatal(err)
	}
	v, err := store.Decode[Delivery](d)
	if err != nil || v.Status != want {
		t.Fatalf("%s: status=%s want=%s error=%v", id, v.Status, want, err)
	}
}
func TestRecipientsPermissionChangesAndEdgeChannels(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	user(t, s, "alice", "operations", "gas-1", true, false)
	user(t, s, "bob", "maintenance", "gas-1", true, false)
	user(t, s, "outsider", "operations", "foreign", true, false)
	user(t, s, "disabled", "operations", "gas-1", false, false)
	user(t, s, "assistant", "operations", "gas-1", true, true)
	if err := s.Deliver(ctx, message("department", "in_app", "department:operations", "user:alice")); err != nil {
		t.Fatal(err)
	}
	status(t, s, "department:alice", "delivered")
	docs, _ := s.Store.List(ctx, "notification")
	if len(docs) != 1 {
		t.Fatalf("recipient filtering: %d", len(docs))
	}
	if err := s.Deliver(ctx, message("department", "in_app", "department:operations", "user:alice")); err != nil {
		t.Fatal(err)
	}
	docs, _ = s.Store.List(ctx, "notification")
	if len(docs) != 1 {
		t.Fatal("duplicate delivery")
	}
	doc, _ := s.Store.Get(ctx, "user", "alice")
	u, _ := store.Decode[model.User](doc)
	u.Active = false
	if _, err := s.Store.Put(ctx, "user", u.ID, doc.Version, u); err != nil {
		t.Fatal(err)
	}
	if err := s.Deliver(ctx, message("after-disable", "in_app", "department:operations")); err != nil {
		t.Fatal(err)
	}
	status(t, s, "after-disable", "suppressed")
	s.Edge = true
	if err := s.Deliver(ctx, message("edge", "sms", "site")); err != nil {
		t.Fatal(err)
	}
	status(t, s, "edge:bob", "delegated_to_cloud")
}

func TestActualHTTPChannelAcknowledgementRetryAndLostResponse(t *testing.T) {
	s := fixture(t)
	user(t, s, "alice", "operations", "gas-1", true, false)
	ctx := context.Background()
	var requests atomic.Int64
	var mode atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer private-test-token" || r.Header.Get("Idempotency-Key") == "" {
			t.Error("missing authenticated delivery identity")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["phone"] != "test:alice" {
			t.Error("invalid SMS payload")
		}
		switch mode.Load() {
		case 1:
			w.WriteHeader(429)
		case 2:
			connection, _, _ := w.(http.Hijacker).Hijack()
			connection.Close()
		case 3:
			w.WriteHeader(503)
		default:
			w.WriteHeader(202)
		}
	}))
	defer server.Close()
	s.Sender = &Channels{Config: settings{"notification.sms": {Enabled: true, Endpoint: server.URL, Token: "private-test-token", TimeoutMS: 1000}}}
	for i := 0; i < 2; i++ {
		if err := s.Deliver(ctx, message("success", "sms", "user:alice")); err != nil {
			t.Fatal(err)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("duplicate success: %d", requests.Load())
	}
	status(t, s, "success:alice", "delivered")
	mode.Store(1)
	if err := s.Deliver(ctx, message("retry", "sms", "user:alice")); err == nil {
		t.Fatal("definite rejection accepted")
	}
	status(t, s, "retry:alice", "pending")
	mode.Store(0)
	if err := s.Deliver(ctx, message("retry", "sms", "user:alice")); err != nil {
		t.Fatal(err)
	}
	status(t, s, "retry:alice", "delivered")
	for _, test := range []struct {
		id   string
		mode int64
	}{{"lost", 2}, {"server-error", 3}} {
		mode.Store(test.mode)
		before := requests.Load()
		_ = s.Deliver(ctx, message(test.id, "sms", "site"))
		status(t, s, test.id+":alice", "result_unknown")
		if err := s.Deliver(ctx, message(test.id, "sms", "site")); err != nil {
			t.Fatal(err)
		}
		if requests.Load() != before+1 {
			t.Fatal("uncertain delivery resent")
		}
	}
}

func TestDatabaseFailurePreventsSendAndInterruptedAcknowledgementDoesNotResend(t *testing.T) {
	s := fixture(t)
	user(t, s, "alice", "operations", "gas-1", true, false)
	ctx := context.Background()
	var sent atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sent.Add(1); w.WriteHeader(202) }))
	defer server.Close()
	s.Sender = &Channels{Config: settings{"notification.sms": {Enabled: true, Endpoint: server.URL}}}
	_, err := s.Store.DB.Exec(`CREATE TRIGGER fail_notification BEFORE INSERT ON documents WHEN NEW.kind='notification' BEGIN SELECT RAISE(ABORT,'injected storage failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Deliver(ctx, message("blocked", "sms", "site")); err == nil || sent.Load() != 0 {
		t.Fatal("sent before durable reservation", err, sent.Load())
	}
	if _, err = s.Store.DB.Exec(`DROP TRIGGER fail_notification`); err != nil {
		t.Fatal(err)
	}
	_, err = s.Store.DB.Exec(`CREATE TRIGGER fail_ack BEFORE UPDATE ON documents WHEN NEW.kind='notification' AND NEW.data LIKE '%"status":"delivered"%' BEGIN SELECT RAISE(ABORT,'injected acknowledgement failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Deliver(ctx, message("interrupted", "sms", "site")); err == nil || sent.Load() != 1 {
		t.Fatal("fault did not interrupt acknowledgement", err, sent.Load())
	}
	status(t, s, "interrupted:alice", "sending")
	if _, err = s.Store.DB.Exec(`DROP TRIGGER fail_ack`); err != nil {
		t.Fatal(err)
	}
	restarted := &Service{Store: s.Store, Identity: s.Identity, Sender: s.Sender}
	if err = restarted.Deliver(ctx, message("interrupted", "sms", "site")); err != nil {
		t.Fatal(err)
	}
	status(t, s, "interrupted:alice", "result_unknown")
	if sent.Load() != 1 {
		t.Fatal("interrupted delivery resent")
	}
}

func TestActualSMTPAcceptedAndUnacknowledgedMessage(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mode atomic.Bool
	var mu sync.Mutex
	bodies := []string{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer connection.Close()
				connection.SetDeadline(time.Now().Add(3 * time.Second))
				p := textproto.NewConn(connection)
				p.PrintfLine("220 localhost test receiver")
				for {
					line, err := p.ReadLine()
					if err != nil {
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
						p.PrintfLine("250 localhost")
					case strings.HasPrefix(line, "MAIL FROM"), strings.HasPrefix(line, "RCPT TO"):
						p.PrintfLine("250 OK")
					case line == "DATA":
						p.PrintfLine("354 Start mail")
						body, err := p.ReadDotBytes()
						if err != nil {
							return
						}
						mu.Lock()
						bodies = append(bodies, string(body))
						mu.Unlock()
						if mode.Load() {
							return
						}
						p.PrintfLine("250 Queued")
					case line == "QUIT":
						p.PrintfLine("221 Bye")
						return
					default:
						p.PrintfLine("500 Unsupported")
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { listener.Close(); <-done })
	s := fixture(t)
	user(t, s, "alice", "operations", "gas-1", true, false)
	ctx := context.Background()
	s.Sender = &Channels{Config: settings{"notification.email": {Enabled: true, SMTPAddress: listener.Addr().String(), SMTPTLSMode: "none", From: "factory@example.test", TimeoutMS: 1000}}}
	if err = s.Deliver(ctx, message("mail", "email", "site")); err != nil {
		t.Fatal(err)
	}
	status(t, s, "mail:alice", "delivered")
	if err = s.Deliver(ctx, message("mail", "email", "site")); err != nil {
		t.Fatal(err)
	}
	mode.Store(true)
	_ = s.Deliver(ctx, message("mail-lost", "email", "site"))
	status(t, s, "mail-lost:alice", "result_unknown")
	if err = s.Deliver(ctx, message("mail-lost", "email", "site")); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 || !strings.Contains(bodies[0], "Message-ID: <"+store.Hash("mail:alice")+"@smartfactory.local>") || !strings.Contains(bodies[0], "gas-1") {
		t.Fatalf("SMTP delivery mismatch: %v", bodies)
	}
}
