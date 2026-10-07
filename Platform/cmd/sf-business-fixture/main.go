// sf-business-fixture runs isolated business APIs and actual rule evaluation.
package main

import (
	"context"
	"crypto/rand"
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
	"syscall"
	"time"

	"competition2026/product/platform/internal/api"
	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/businessfixture"
	"competition2026/product/platform/internal/configcenter"
	"competition2026/product/platform/internal/control"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	dir := flag.String("dir", "", "new isolated state directory")
	address := flag.String("address", "127.0.0.1:19310", "loopback HTTP address")
	mode := flag.String("mode", "cloud", "cloud or edge")
	static := flag.String("static", "", "optional built frontend directory")
	duration := flag.Duration("duration", 0, "optional run duration")
	flag.Parse()
	if *dir == "" || (*mode != "cloud" && *mode != "edge") {
		return errors.New("new -dir and cloud/edge -mode required")
	}
	host, _, e := net.SplitHostPort(*address)
	if e != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("address must be a loopback IP")
	}
	if _, e = os.Stat(*dir); !errors.Is(e, os.ErrNotExist) {
		return errors.New("fixture directory already exists; select a new directory")
	}
	if e = os.MkdirAll(*dir, 0700); e != nil {
		return e
	}
	secret := make([]byte, 24)
	if _, e = rand.Read(secret); e != nil {
		return e
	}
	password := hex.EncodeToString(secret)
	master := make([]byte, 32)
	if _, e = rand.Read(master); e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(*dir, "master.key"), master, 0600); e != nil {
		return e
	}
	save := func(name string, value any) error {
		raw, e := json.MarshalIndent(value, "", "  ")
		if e != nil {
			return e
		}
		return os.WriteFile(filepath.Join(*dir, name), append(raw, '\n'), 0600)
	}
	if e = save("private-credentials.json", map[string]any{"login": "operator-a", "other_login": "operator-b", "password": password}); e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, e := store.Open(ctx, filepath.Join(*dir, "business.db"), "edge-a", master)
	if e != nil {
		return e
	}
	defer db.Close()
	db.DB.SetMaxOpenConns(1)
	db.DB.SetMaxIdleConns(1)
	f, e := businessfixture.Seed(ctx, db, master, password)
	if e != nil {
		return e
	}
	f.Business.Mode = *mode
	s := &api.Server{Store: db, Identity: f.Identity, Engine: f.Engine, Mode: *mode, NodeID: "edge-a", StaticDir: *static, Control: &control.Service{Store: db, Identity: f.Identity, Definitions: f.Engine, NodeID: "edge-a", Edge: *mode == "edge"}, Config: &configcenter.Service{Store: db, Identity: f.Identity}}
	alarm, e := f.Business.AlarmDetail(ctx, f.Principal, f.AlarmID)
	if e != nil {
		return e
	}
	for _, input := range []application.AlarmActionInput{{RequestID: "fixture-assign", Action: "assign", AssigneeID: f.Principal.User.ID, Reason: "由当前班次跟进温度告警"}, {RequestID: "fixture-note", Action: "note", Reason: "现场已检查传感器，后续持续观察温度"}} {
		input.ExpectedVersion = alarm.Case.Version
		input.ExpectedActionVersion = alarm.ActionVersion
		alarm, e = f.Business.ActOnAlarm(ctx, f.Principal, f.AlarmID, input)
		if e != nil {
			return e
		}
	}
	work, e := f.Business.CreateWorkOrder(ctx, f.Principal, application.CreateWorkOrderInput{ID: "fixture-work-order", RequestID: "fixture-work-order", Title: "检查通风与温度采样", Description: "现场确认通风设备，记录后续温度变化并安排交班", GroupID: "factory", AssigneeID: f.Principal.User.ID, AlarmIDs: []string{f.AlarmID}, Reason: "持续温度告警需要现场检查"})
	if e != nil {
		return e
	}
	batchInput := model.TemplateBatchInput{ID: "fixture-five-scenes", RequestID: "fixture-five-scenes", GroupID: "factory", Instances: f.Instances}
	var batch *model.TemplateBatch
	if *mode == "cloud" {
		v, e := f.Business.PrepareTemplateBatch(ctx, f.Principal, batchInput)
		if e != nil {
			return e
		}
		batch = &v
	}
	protocols, e := f.Business.DeviceProtocols(ctx, f.Principal)
	if e != nil {
		return e
	}
	templates, e := f.Business.Templates(ctx, f.Principal)
	if e != nil {
		return e
	}
	if e = save("samples.json", map[string]any{"alarm_detail": alarm, "work_order": work, "protocols": protocols, "templates": templates, "template_batch_input": batchInput, "prepared_batch": batch}); e != nil {
		return e
	}
	listener, e := net.Listen("tcp", *address)
	if e != nil {
		return e
	}
	server := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	if e = save("manifest.json", map[string]any{"created_at": time.Now().UTC(), "mode": *mode, "url": "http://" + listener.Addr().String(), "node_id": "edge-a", "alarm_id": f.AlarmID, "draft_id": f.DraftID, "work_order_id": work.WorkOrder.ID, "template_batch_id": batchInput.ID, "credentials": "private-credentials.json", "samples": "samples.json", "sampling_interval_ms": 1000, "sampling_value": 35, "isolation": "new SQLite database; native adapter and cross-node synchronization disabled for this standalone UI fixture"}); e != nil {
		return e
	}
	fmt.Printf("Business fixture ready at http://%s; manifest=%s; credentials=%s\n", listener.Addr(), filepath.Join(*dir, "manifest.json"), filepath.Join(*dir, "private-credentials.json"))
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var timer *time.Timer
	var deadline <-chan time.Time
	if *duration > 0 {
		timer = time.NewTimer(*duration)
		defer timer.Stop()
		deadline = timer.C
	}
	for {
		select {
		case now := <-ticker.C:
			if e = f.Observe(ctx, 35, now.UnixMilli()); e != nil && ctx.Err() == nil {
				stop()
				_ = server.Close()
				return e
			}
		case <-deadline:
			stop()
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdown)
			_ = server.Close()
			e = <-done
			if errors.Is(e, http.ErrServerClosed) {
				return nil
			}
			return e
		case e = <-done:
			if errors.Is(e, http.ErrServerClosed) {
				return nil
			}
			return e
		}
	}
}
