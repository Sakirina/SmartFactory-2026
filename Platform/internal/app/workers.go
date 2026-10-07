package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"competition2026/product/platform/internal/notifications"
	"competition2026/product/platform/internal/plugins"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/internal/tasks"
	"competition2026/product/platform/pkg/model"
)

func (a *Application) worker(ctx context.Context, interval time.Duration, fn func(context.Context) error) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		timer := time.NewTimer(interval)
		defer timer.Stop()
		retry := time.Duration(0)
		for {
			started := time.Now()
			delay := interval
			if e := fn(ctx); e != nil && !errors.Is(e, context.Canceled) {
				slog.Error("background task failed", "error", e)
				// An unavailable remote service must not turn a large pending
				// queue into continuous failed database updates.
				retry = min(max(time.Second, retry*2), 30*time.Second)
				delay = max(interval, retry)
			} else {
				retry = 0
				delay = max(0, interval-time.Since(started))
			}
			timer.Reset(delay)
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
		}
	}()
}
func (a *Application) startWorkers(ctx context.Context) error {
	a.startReleaseCoordinator(ctx)
	if a.Options.Mode == "config" {
		return nil
	}
	if a.Native != nil {
		if err := a.Store.Write(ctx, func(tx *store.Tx) error {
			return tx.Enqueue(fmt.Sprintf("native-reconcile:%s:%d", a.Options.NodeID, time.Now().UnixNano()), "tb_reconcile", a.Options.NodeID, map[string]string{"node_id": a.Options.NodeID})
		}); err != nil {
			return err
		}
	}
	handlers := tasks.Handlers{Recompute: a.Server.Engine.Recompute, Archive: a.Store.ArchiveObservations, Analysis: a.Server.HistoryApplication().Execute}
	if a.Native != nil {
		handlers.Projection = a.projectDelivery
	}
	scheduler, err := tasks.New(a.Store, handlers)
	if err != nil {
		return err
	}
	a.Tasks = scheduler
	if err = scheduler.Start(ctx); err != nil {
		return err
	}
	if a.Options.Mode == "cloud" {
		manager := &plugins.Manager{Store: a.Store, Config: a.Server.Config}
		a.worker(ctx, time.Second, manager.Poll)
	}
	if a.Site != nil {
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			if e := a.Site.Run(ctx, a.Server.Control); e != nil && ctx.Err() == nil {
				slog.Error("site coordination stopped", "error", e)
			}
		}()
	}
	if a.SyncClient != nil {
		a.worker(ctx, 200*time.Millisecond, a.SyncClient.Exchange)
	}
	if a.SyncServer != nil {
		a.worker(ctx, 2*time.Second, a.SyncServer.CompleteMetadata)
	}
	if a.Options.ConfigURL != "" {
		if err = a.StartNodeConfiguration(ctx); err != nil {
			return err
		}
	} else {
		a.worker(ctx, time.Second, func(ctx context.Context) error {
			if err := a.applyPolicy(ctx); err != nil {
				return err
			}
			parameters, err := a.Server.Config.List(ctx)
			if err != nil {
				return err
			}
			for _, parameter := range parameters {
				if parameter.Effective[a.Options.NodeID] != parameter.Version {
					if err = a.Server.Config.Acknowledge(ctx, a.Options.NodeID, parameter.ID, parameter.Version, true, ""); err != nil {
						return err
					}
				}
			}
			return nil
		})
	}
	a.startBusinessWorkers(ctx)
	a.worker(ctx, time.Hour, a.retention)
	a.worker(ctx, time.Minute, func(ctx context.Context) error { return a.Store.CompactQueryHistory(ctx, store.QueryHistoryCommits) })
	if a.Options.Mode == "edge" {
		a.worker(ctx, 100*time.Millisecond, func(ctx context.Context) error {
			return a.consume(ctx, "edge_control_operation", 20, func(d store.Delivery) error {
				var envelope model.ControlOperationEnvelope
				if err := store.DecodeJSON(d.Payload, &envelope); err != nil {
					return err
				}
				return a.Server.Control.ProcessOperation(ctx, envelope)
			})
		})
		a.worker(ctx, 100*time.Millisecond, a.Server.Engine.Tick)
	}
	if a.Bridge != nil {
		a.wg.Add(1)
		go func() { defer a.wg.Done(); _ = a.Bridge.Run(ctx) }()
		a.worker(ctx, time.Second, func(ctx context.Context) error {
			return a.consume(ctx, "device_config", 32, func(item store.Delivery) error {
				var entity model.Entity
				if e := store.DecodeJSON(item.Payload, &entity); e != nil {
					return e
				}
				return a.Bridge.ApplyConfig(ctx, entity)
			})
		})
	}

	a.worker(ctx, 20*time.Millisecond, func(ctx context.Context) error {
		return a.consume(ctx, "strategy", 256, func(d store.Delivery) error {
			var p model.Observation
			if err := store.DecodeJSON(d.Payload, &p); err != nil {
				return err
			}
			return a.Server.Engine.ProcessStrategies(ctx, p)
		})
	})
	a.worker(ctx, 50*time.Millisecond, func(ctx context.Context) error {
		return a.consume(ctx, "engine", 256, func(d store.Delivery) error {
			var p model.Observation
			if e := store.DecodeJSON(d.Payload, &p); e != nil {
				return e
			}
			historical := p.Late || a.Store.Now().UnixMilli()-p.ObservedMS > 15000
			return a.Server.Engine.ProcessCalculations(ctx, p, historical)
		})
	})
	a.worker(ctx, time.Second, func(ctx context.Context) error {
		return a.consume(ctx, "notification", 100, func(d store.Delivery) error {
			notifier := notifications.Service{Store: a.Store, Identity: a.Server.Identity, Sender: &notifications.Channels{Config: a.Server.Config}, Edge: a.Options.Mode == "edge"}
			return notifier.Deliver(ctx, d.Payload)
		})
	})

	if a.Options.Mode == "edge" {
		a.worker(ctx, 100*time.Millisecond, func(ctx context.Context) error {
			return a.consume(ctx, "edge_downlink", 20, func(d store.Delivery) error {
				var req model.Execution
				if e := store.DecodeJSON(d.Payload, &req); e != nil {
					return e
				}
				_, e := a.Server.Control.Run(ctx, req, false)
				return e
			})
		})
		a.worker(ctx, 100*time.Millisecond, func(ctx context.Context) error {
			return a.consume(ctx, "strategy_trigger", 20, func(d store.Delivery) error {
				var trigger struct {
					DefinitionID string `json:"definition_id"`
					Version      int64  `json:"version"`
				}
				if e := store.DecodeJSON(d.Payload, &trigger); e != nil {
					return e
				}
				req := model.Execution{DownlinkID: d.ID, DefinitionID: trigger.DefinitionID, DefinitionVersion: trigger.Version, Status: "queued", Mode: "automatic", StartDeadlineMS: d.CreatedMS + a.Store.Policy().StartTTLMS, Actor: model.Actor{UserID: "published-policy", Source: a.Options.NodeID}, Steps: []model.StepResult{}, Approvals: []model.Approval{}, Params: map[string]string{}}
				_, e := a.Server.Control.Run(ctx, req, true)
				return e
			})
		})
		a.worker(ctx, time.Second, a.schedule)
		a.worker(ctx, 100*time.Millisecond, func(ctx context.Context) error {
			return a.consume(ctx, "scheduled_execution", 20, func(d store.Delivery) error {
				var req model.Execution
				if e := store.DecodeJSON(d.Payload, &req); e != nil {
					return e
				}
				_, e := a.Server.Control.Run(ctx, req, true)
				return e
			})
		})
	}
	return nil
}
func (a *Application) consume(ctx context.Context, kind string, limit int, fn func(store.Delivery) error) error {
	items, e := a.Store.Deliveries(ctx, kind, limit)
	if e != nil {
		return e
	}
	completed := make([]string, 0, len(items))
	failed := map[string]error{}
	for _, d := range items {
		if e = ctx.Err(); e != nil {
			break
		}
		if err := fn(d); err != nil {
			failed[d.ID] = err
			slog.Warn("delivery deferred", "kind", kind, "id", d.ID, "error", err)
		} else {
			completed = append(completed, d.ID)
		}
	}
	return errors.Join(e, a.Store.CompleteDeliveries(ctx, completed, failed))
}
func (a *Application) schedule(ctx context.Context) error {
	docs, e := a.Store.List(ctx, "definition")
	if e != nil {
		return e
	}
	for _, doc := range docs {
		d, e := store.Decode[model.Definition](doc)
		if e != nil {
			return e
		}
		if d.Status != "published" || d.Kind != "strategy" || d.Policy.Schedule == nil {
			continue
		}
		sc := d.Policy.Schedule
		stateID := fmt.Sprintf("%s:%d", d.ID, d.Version)
		next := sc.NextMS
		if next <= 0 {
			next = (a.Store.Now().UnixMilli()/sc.EveryMS + 1) * sc.EveryMS
		}
		if saved, e := a.Store.Get(ctx, "schedule", stateID); e == nil {
			var state struct {
				NextMS int64 `json:"next_ms"`
			}
			if e = json.Unmarshal(saved.Data, &state); e != nil {
				return e
			}
			next = state.NextMS
		}
		now := a.Store.Now().UnixMilli()
		if next <= 0 || next > now {
			continue
		}
		due := next
		next += ((now-next)/sc.EveryMS + 1) * sc.EveryMS
		req := model.Execution{DownlinkID: fmt.Sprintf("schedule:%s:%d:%d", d.ID, d.Version, due), DefinitionID: d.ID, DefinitionVersion: d.Version, Status: "queued", Mode: "scheduled", StartDeadlineMS: due + sc.WindowMS, Actor: model.Actor{UserID: "published-policy", Source: a.Options.NodeID}, Params: map[string]string{}, Steps: []model.StepResult{}, Approvals: []model.Approval{}}
		e = a.Store.Write(ctx, func(t *store.Tx) error {
			if e := t.SetEphemeral("schedule", stateID, map[string]any{"next_ms": next}); e != nil {
				return e
			}
			if now > due+sc.WindowMS {
				return t.Audit(req.Actor, "schedule.skipped", d.ID, req.DownlinkID, map[string]any{"scheduled_ms": due, "observed_ms": now, "reason": "execution window missed"})
			}
			return t.Enqueue(req.DownlinkID, "scheduled_execution", a.Options.NodeID, req)
		})
		if e != nil {
			return e
		}
	}
	return nil
}

// River owns external projection acquisition and retry. Adapter calls retain
// the original outbox identity and authoritative business payload.
func (a *Application) projectDelivery(ctx context.Context, item store.Delivery) error {
	a.projectionMu.Lock()
	if !a.projectionReady {
		if err := a.Native.ReconcileDefinitions(ctx); err != nil {
			a.projectionMu.Unlock()
			return err
		}
		if err := a.Native.InstallRoot(ctx); err != nil {
			a.projectionMu.Unlock()
			return err
		}
		a.projectionReady = true
	}
	a.projectionMu.Unlock()
	switch item.Kind {
	case "tb_reconcile":
		return nil
	case "tb_alarm":
		var alarm model.Alarm
		if err := store.DecodeJSON(item.Payload, &alarm); err != nil {
			return err
		}
		return a.Native.Alarm(ctx, alarm)
	case "tb_entity":
		var entity model.Entity
		if err := store.DecodeJSON(item.Payload, &entity); err != nil {
			return err
		}
		_, err := a.Native.EnsureEntity(ctx, entity)
		return err
	case "tb_definition":
		var definition model.Definition
		if err := store.DecodeJSON(item.Payload, &definition); err != nil {
			return err
		}
		if err := a.Native.Definition(ctx, definition); err != nil {
			return err
		}
		return a.Native.InstallRoot(ctx)
	case "tb_telemetry":
		var point model.Observation
		if err := store.DecodeJSON(item.Payload, &point); err != nil {
			return err
		}
		return a.Native.Project(ctx, []model.Observation{point})
	}
	return fmt.Errorf("unsupported projection kind %s", item.Kind)
}
