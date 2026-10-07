// sf-ai-evidence-fixture runs isolated business data, assistant HTTP and a model.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"competition2026/product/platform/internal/ai"
	"competition2026/product/platform/internal/aievidencefixture"
	"competition2026/product/platform/internal/api"
	"competition2026/product/platform/internal/businessfixture"
	"competition2026/product/platform/internal/configcenter"
	"competition2026/product/platform/internal/control"
	"competition2026/product/platform/internal/observability"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	dir := flag.String("dir", "", "new isolated state directory")
	address := flag.String("address", "127.0.0.1:0", "loopback application address")
	apiKind := flag.String("model-api", "responses", "responses or chat_completions")
	stream := flag.Bool("model-stream", true, "model streaming")
	static := flag.String("static", "", "optional built frontend directory")
	duration := flag.Duration("duration", 0, "optional run duration")
	flag.Parse()
	if *dir == "" || (*apiKind != "responses" && *apiKind != "chat_completions") {
		return errors.New("new -dir and valid -model-api required")
	}
	host, _, err := net.SplitHostPort(*address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("application address must be a loopback IP")
	}
	if _, err = os.Stat(*dir); !errors.Is(err, os.ErrNotExist) {
		return errors.New("fixture directory already exists; select a new directory")
	}
	if err = os.MkdirAll(*dir, 0700); err != nil {
		return err
	}
	secret := make([]byte, 24)
	if _, err = rand.Read(secret); err != nil {
		return err
	}
	password := hex.EncodeToString(secret)
	master := make([]byte, 32)
	if _, err = rand.Read(master); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(*dir, "master.key"), master, 0600); err != nil {
		return err
	}
	save := func(name string, value any) error {
		raw, e := json.MarshalIndent(value, "", "  ")
		if e != nil {
			return e
		}
		return os.WriteFile(filepath.Join(*dir, name), append(raw, '\n'), 0600)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := store.Open(ctx, filepath.Join(*dir, "investigation.db"), "edge-a", master)
	if err != nil {
		return err
	}
	defer db.Close()
	db.DB.SetMaxOpenConns(1)
	db.DB.SetMaxIdleConns(1)
	f, err := businessfixture.Seed(ctx, db, master, password)
	if err != nil {
		return err
	}
	config := &configcenter.Service{Store: db, Identity: f.Identity}
	s := &api.Server{Store: db, Identity: f.Identity, Engine: f.Engine, Mode: "cloud", NodeID: "edge-a", StaticDir: *static, Config: config, Control: &control.Service{Store: db, Identity: f.Identity, Definitions: f.Engine, NodeID: "edge-a"}}
	plan, err := aievidencefixture.Seed(ctx, s, f)
	if err != nil {
		return err
	}
	transcript, err := os.OpenFile(filepath.Join(*dir, "model-visible-results.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer transcript.Close()
	var transcriptMu sync.Mutex
	seen := map[string]bool{}
	modelServer := &aievidencefixture.Model{Plan: plan, OnOutputs: func(outputs []ai.Message) {
		transcriptMu.Lock()
		defer transcriptMu.Unlock()
		for _, output := range outputs {
			sum := sha256.Sum256([]byte(output.Content))
			key := hex.EncodeToString(sum[:])
			if seen[key] {
				continue
			}
			seen[key] = true
			_ = json.NewEncoder(transcript).Encode(map[string]any{"tool_call_id": output.ToolCallID, "visible_sha256": key, "visible_content": output.Content})
		}
	}}
	modelListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	modelHTTP := &http.Server{Handler: modelServer, ReadHeaderTimeout: 10 * time.Second}
	defer modelHTTP.Close()
	go func() { _ = modelHTTP.Serve(modelListener) }()
	tools := &ai.Tools{API: s}
	s.MCP = &ai.MCP{Tools: tools}
	modelConfig := ai.ModelConfig{Provider: "openai", API: *apiKind, Stream: stream, Endpoint: "http://" + modelListener.Addr().String() + "/v1", Model: "local-evidence-fixture", APIKey: password, TimeoutMS: 5000}
	s.Chat = &ai.Chat{Tools: tools, Config: config, Investigations: s.InvestigationApplication(), Default: modelConfig}
	if err = save("private-credentials.json", map[string]any{"login": "operator-a", "other_login": "operator-b", "password": password, "model": modelConfig}); err != nil {
		return err
	}
	apiHandler := s.Handler()
	fixture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/__fixture/state" {
			apiHandler.ServeHTTP(w, r)
			return
		}
		p, e := s.Authenticate(r)
		if e != nil || p.User.ID != "operator-a" {
			http.Error(w, "fixture owner required", 403)
			return
		}
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var in struct {
			Action          string `json:"action"`
			InvestigationID string `json:"investigation_id"`
		}
		if e = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); e != nil {
			http.Error(w, "invalid scenario", 400)
			return
		}
		switch in.Action {
		case "revoke", "restore":
			user := p.User
			user.Resources = []string{"private-factory"}
			if in.Action == "restore" {
				user.Resources = []string{"factory"}
			}
			_, e = f.Identity.CreateUser(r.Context(), p.Actor, user, "", "", user.Version)
		case "expire":
			var value model.Investigation
			value, e = db.Investigation(r.Context(), in.InvestigationID)
			if e == nil && value.UserID != p.User.ID {
				e = store.ErrNotFound
			}
			if e == nil {
				var items []model.InvestigationEvidence
				items, e = db.InvestigationEvidenceList(r.Context(), value.ID)
				if e == nil {
					value.ExpiresMS = time.Now().Add(-time.Minute).UnixMilli()
					value.Version++
					e = db.Write(r.Context(), func(tx *store.Tx) error {
						if e := tx.PutInvestigation(value, value.Version-1); e != nil {
							return e
						}
						for _, item := range items {
							item.ExpiresMS = value.ExpiresMS
							if e := tx.PutInvestigationEvidence(item, false); e != nil {
								return e
							}
						}
						return nil
					})
				}
			}
		case "delete_resource":
			var doc store.Document
			doc, e = db.Get(r.Context(), "definition", plan.DefinitionID)
			if e == nil {
				e = db.Write(r.Context(), func(tx *store.Tx) error { return tx.Delete("definition", doc.ID) })
			}
		case "prepare_original_resource", "rebind_definition":
			if in.Action == "prepare_original_resource" {
				user := p.User
				user.Resources = []string{"factory", "private-factory"}
				_, e = f.Identity.CreateUser(r.Context(), p.Actor, user, "", "", user.Version)
			}
			if e == nil {
				var doc store.Document
				doc, e = db.Get(r.Context(), "definition", plan.DefinitionID)
				if e == nil {
					var definition model.Definition
					definition, e = store.Decode[model.Definition](doc)
					if e == nil {
						definition.GroupID = "factory"
						if in.Action == "prepare_original_resource" {
							definition.GroupID = "private-factory"
							_, e = db.Get(r.Context(), "entity", definition.GroupID)
						}
						if e == nil {
							definition.Version++
							_, e = db.Put(r.Context(), "definition", definition.ID, doc.Version, definition)
						}
					}
				}
			}
		case "delete_original_resource":
			e = db.Write(r.Context(), func(tx *store.Tx) error { return tx.Delete("entity", "private-factory") })
		default:
			e = errors.New("invalid fixture scenario")
		}
		if e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"action": in.Action, "applied": true})
	})
	listener, err := net.Listen("tcp", *address)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: observability.HTTPContext(fixture), ReadHeaderTimeout: 10 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	defer server.Close()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	if err = save("manifest.json", map[string]any{"created_at": time.Now().UTC(), "url": "http://" + listener.Addr().String(), "model_api": *apiKind, "model_stream": *stream, "definition_id": plan.DefinitionID, "device_id": plan.DeviceID, "execution_id": plan.ExecutionID, "analysis_run_id": plan.RunID, "oversized_id": plan.OversizedID, "alarm_id": f.AlarmID, "credentials": "private-credentials.json", "model_visible_results": "model-visible-results.jsonl", "assistant": "/api/sf/v1/assistant", "fixture_state": "/__fixture/state", "fixture_actions": []string{"revoke", "restore", "expire", "delete_resource", "prepare_original_resource", "rebind_definition", "delete_original_resource"}, "commands": []string{"调查温度、执行与历史依据", "读取身份 调查/温度:一号", "伪造引用", "跨调查引用", "空查询", "查询失败", "超预算", "无依据回答", "模拟中断", "模拟服务错误", "模拟超时"}}); err != nil {
		return err
	}
	fmt.Printf("AI evidence fixture ready at http://%s; manifest=%s\n", listener.Addr(), filepath.Join(*dir, "manifest.json"))
	var deadline <-chan time.Time
	if *duration > 0 {
		timer := time.NewTimer(*duration)
		defer timer.Stop()
		deadline = timer.C
	}
	select {
	case err = <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-deadline:
		stop()
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdown)
	_ = modelHTTP.Shutdown(shutdown)
	return nil
}
