// sf-site-fixture runs isolated application processes for the 150-device site test.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"competition2026/product/platform/internal/app"
	"competition2026/product/platform/internal/coordination"
	"competition2026/product/platform/internal/plugins"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type configuration struct {
	Directory string `json:"directory"`
	Password  string `json:"password"`
	Token     string `json:"token"`
	NATSURL   string `json:"nats_url"`
	Prefix    string `json:"prefix"`
	Nodes     []node `json:"nodes"`
}
type node struct {
	ID      string `json:"id"`
	DSN     string `json:"dsn"`
	Listen  string `json:"listen"`
	Gateway string `json:"gateway"`
}

func main() {
	file := flag.String("config", "", "private fixture configuration")
	mode := flag.String("mode", "run", "seed or run")
	id := flag.String("node", "", "node to run")
	execution := flag.String("execution-id", "", "automatic execution identifier for enqueue mode")
	flag.Parse()
	err := run(*file, *mode, *id, *execution)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(file, mode, id, execution string) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	var cfg configuration
	if err = json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	if len(cfg.Nodes) != 3 || len(cfg.Password) < 12 {
		return errors.New("three fixture nodes and a private password required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	open := func(n node, network bool) (*app.Application, error) {
		o := app.Options{Mode: "edge", NodeID: n.ID, DSN: n.DSN, KeyFile: filepath.Join(cfg.Directory, n.ID+".master"), BootstrapPassword: cfg.Password, Address: n.Listen}
		if network {
			o.DataTransferAddress = n.Gateway
			o.NATSURL = cfg.NATSURL
			o.NATSToken = cfg.Token
			o.NATSPrefix = cfg.Prefix
			o.NATSCA = filepath.Join(cfg.Directory, "pki/ca.pem")
			o.NATSCertificate = filepath.Join(cfg.Directory, "pki", n.ID+".pem")
			o.NATSKey = filepath.Join(cfg.Directory, "pki", n.ID+".key")
		}
		return app.Open(ctx, o)
	}
	if mode == "run" {
		for _, n := range cfg.Nodes {
			if n.ID == id {
				a, e := open(n, true)
				if e != nil {
					return e
				}
				defer a.Close()
				a.Site.Options.LeaseTTL = 5 * time.Second
				return a.Run(ctx)
			}
		}
		return errors.New("unknown fixture node")
	}
	if mode == "ready" {
		for _, n := range cfg.Nodes {
			if n.ID == id {
				a, e := open(n, true)
				if e != nil {
					return e
				}
				defer a.Close()
				c, e := coordination.Open(ctx, a.Store, a.Site.Options)
				if e != nil {
					return e
				}
				defer c.Close()
				d, e := a.Server.Engine.Published(ctx, "site-gas-plan", 0)
				if e != nil {
					return e
				}
				if e = c.Ready(ctx, d); e != nil {
					return e
				}
				check, e := a.Server.Control.Conditions(ctx, d)
				if e != nil {
					return e
				}
				if !check.Allowed || check.Interlocked {
					return fmt.Errorf("fixture observations are not ready: %v", check.Reasons)
				}
				return nil
			}
		}
		return errors.New("unknown fixture node")
	}
	if mode == "enqueue" {
		if execution == "" {
			return errors.New("execution identifier required")
		}
		for _, n := range cfg.Nodes {
			if n.ID == id {
				a, e := open(n, false)
				if e != nil {
					return e
				}
				defer a.Close()
				d, e := a.Server.Engine.Published(ctx, "site-gas-plan", 0)
				if e != nil {
					return e
				}
				req := model.Execution{DownlinkID: execution, DefinitionID: d.ID, DefinitionVersion: d.Version, Status: "queued", StartDeadlineMS: time.Now().Add(10 * time.Second).UnixMilli(), Actor: model.Actor{UserID: "published-policy", Source: "acceptance-fixture"}, Params: map[string]string{}, Steps: []model.StepResult{}, Approvals: []model.Approval{}}
				return a.Store.Write(ctx, func(t *store.Tx) error { return t.Enqueue(execution, "scheduled_execution", id, req) })
			}
		}
		return errors.New("unknown fixture node")
	}
	if mode != "seed" {
		return errors.New("unknown fixture mode")
	}
	apps := []*app.Application{}
	defer func() {
		for _, a := range apps {
			_ = a.Close()
		}
	}()
	for _, n := range cfg.Nodes {
		a, e := open(n, false)
		if e != nil {
			return e
		}
		apps = append(apps, a)
		docs, e := a.Store.List(ctx, "entity")
		if e != nil {
			return e
		}
		if len(docs) > 0 {
			return errors.New("site fixture requires empty business databases")
		}
	}
	actor := model.Actor{UserID: "fixture", Source: "A09-site-acceptance"}
	entities := []model.Entity{{ID: "factory", Name: "三节点仿真工厂", Kind: "asset", Status: "active", Version: 1}}
	for i, n := range cfg.Nodes {
		identity, _ := json.Marshal(map[string]string{"audit_public_key": base64.StdEncoding.EncodeToString(apps[i].Store.SignKey.Public().(ed25519.PublicKey))})
		entities = append(entities, model.Entity{ID: n.ID, Name: n.ID, Kind: "edge", ParentID: "factory", Status: "active", Version: 1, Config: identity})
		for device := 0; device < 50; device++ {
			id := fmt.Sprintf("%s-device-%03d", n.ID, device)
			entities = append(entities, model.Entity{ID: id, Name: id, Kind: "device", ParentID: "factory", EdgeID: n.ID, Protocol: "mqtt", Status: "approved", Version: 1, SamplingMS: 1000})
		}
	}
	d := model.Definition{ID: "site-gas-plan", SchemaVersion: "1.0", Name: "现场危气联动测试预案", Kind: "strategy", GroupID: "factory", Selector: model.Selector{DeviceIDs: []string{"edge-a-device-000"}, Keys: []string{"smoke"}}, Nodes: []model.Node{{ID: "input", Type: "input"}, {ID: "threshold", Type: "threshold", Params: map[string]any{"operator": ">", "value": 100000}}, {ID: "action", Type: "action"}}, Connections: []model.Connection{{From: "input", To: "threshold"}, {From: "threshold", To: "action"}}, Policy: model.Policy{RiskCategory: "business", RiskLevel: 1, EdgeIDs: []string{"edge-a", "edge-b", "edge-c"}}}
	for _, n := range cfg.Nodes {
		device := n.ID + "-device-000"
		d.Policy.Conditions = append(d.Policy.Conditions, model.Condition{DeviceID: device, Key: "interlock", Operator: "==", Value: false, Interlock: true, MaxAgeMS: 15000})
		d.Policy.Steps = append(d.Policy.Steps, model.Step{ID: "extract-" + n.ID, EdgeID: n.ID, DeviceID: device, Action: "set_extractor", Params: map[string]string{"value": "true"}, Idempotent: true, TimeoutMS: 5000})
		d.Policy.Degraded = append(d.Policy.Degraded, model.Step{ID: "local-extract-" + n.ID, EdgeID: n.ID, DeviceID: device, Action: "set_extractor", Params: map[string]string{"value": "true"}, Idempotent: true, TimeoutMS: 5000})
	}
	for i, a := range apps {
		if err = plugins.SyncOrganization(ctx, a.Store, actor, plugins.OrganizationSync{ID: "fixture-org", Source: "fixture", Sequence: 1, Departments: []plugins.Department{{ID: "engineering", Name: "工程技术部", ParentID: "production", ManagerID: "leader"}, {ID: "production", Name: "生产管理部", ManagerID: "leader"}}}); err != nil {
			return err
		}
		for _, u := range []model.User{{ID: "engineer", Login: "engineer", Name: "工程师", Active: true, Roles: []string{"engineer"}, Resources: []string{"factory"}, DepartmentID: "engineering"}, {ID: "leader", Login: "leader", Name: "负责人", Active: true, Roles: []string{"leader"}, Resources: []string{"factory"}, DepartmentID: "production"}} {
			if _, err = a.Server.Identity.CreateUser(ctx, actor, u, cfg.Password, "", 0); err != nil {
				return err
			}
		}
		for _, entity := range entities {
			if _, err = a.Store.Put(ctx, "entity", entity.ID, 0, entity); err != nil {
				return err
			}
		}
		if i == 0 {
			if _, err = a.Server.Engine.SaveDraft(ctx, actor, model.Draft{ID: d.ID, Definition: d}, 0); err != nil {
				return err
			}
			d, err = a.Server.Engine.Publish(ctx, actor, d.ID)
			if err != nil {
				return err
			}
		} else {
			if _, err = a.Store.Put(ctx, "definition", d.ID, 0, d); err != nil {
				return err
			}
		}
		bundle, e := a.Server.Identity.SignBundle(ctx, a.Options.NodeID, 1)
		if e != nil {
			return e
		}
		if err = a.Server.Identity.ApplyBundle(ctx, bundle, a.Store.SignKey.Public().(ed25519.PublicKey)); err != nil {
			return err
		}
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"nodes": 3, "devices_per_node": 50, "definition_hash": store.Hash(d), "lease_ms": 5000, "offline_ms": 15000})
}
