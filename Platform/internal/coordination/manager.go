package coordination

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"competition2026/product/platform/internal/control"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

// Manager keeps local execution available while the site quorum is unavailable,
// including an edge booting before the three JetStream replicas are ready.
type Manager struct {
	Store   *store.Store
	Options Options
	Local   control.Dispatcher
	mu      sync.RWMutex
	current *Coordinator
}

func (m *Manager) active() (*Coordinator, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.current == nil {
		return nil, errors.New("site coordination is unavailable")
	}
	return m.current, nil
}
func (m *Manager) Run(ctx context.Context, service *control.Service) error {
	var c *Coordinator
	for ctx.Err() == nil {
		var err error
		c, err = Open(ctx, m.Store, m.Options)
		if err == nil {
			break
		}
		slog.Warn("site coordination reconnecting", "node", m.Store.NodeID, "error", err)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
	if c == nil {
		return nil
	}
	m.mu.Lock()
	m.current = c
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.current = nil; m.mu.Unlock(); c.Close() }()
	calls, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	repeat := func(interval time.Duration, fn func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				if err := fn(calls); err != nil && calls.Err() == nil {
					slog.Warn("site coordination task failed", "node", m.Store.NodeID, "error", err)
				}
				select {
				case <-calls.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	}
	repeat(time.Second, c.PublishState)
	repeat(time.Second, c.ReceiveStates)
	repeat(2*time.Second, func(ctx context.Context) error { return c.Resume(ctx, service) })
	err := c.ServeCommands(calls, service)
	cancel()
	wg.Wait()
	return err
}
func (m *Manager) Acquire(ctx context.Context, id, owner string, ttl time.Duration) (uint64, func(), error) {
	c, e := m.active()
	if e != nil {
		return 0, nil, e
	}
	return c.Acquire(ctx, id, owner, ttl)
}
func (m *Manager) Ready(ctx context.Context, d model.Definition) error {
	c, e := m.active()
	if e != nil {
		return e
	}
	return c.Ready(ctx, d)
}
func (m *Manager) Validate(ctx context.Context, id, owner string, fence uint64) error {
	c, e := m.active()
	if e != nil {
		return e
	}
	return c.Validate(ctx, id, owner, fence)
}
func (m *Manager) Checkpoint(ctx context.Context, e model.Execution) error {
	c, err := m.active()
	if err != nil {
		return err
	}
	return c.Checkpoint(ctx, e)
}
func (m *Manager) Recover(ctx context.Context, id string) (model.Execution, error) {
	c, e := m.active()
	if e != nil {
		return model.Execution{}, e
	}
	return c.Recover(ctx, id)
}
func (m *Manager) Send(ctx context.Context, step model.Step, id string, deadline int64) (control.DispatchResult, error) {
	if step.EdgeID == m.Store.NodeID {
		if m.Local == nil {
			return control.DispatchResult{}, errors.New("local protocol dispatcher is unavailable")
		}
		return m.Local.Send(ctx, step, id, deadline)
	}
	c, e := m.active()
	if e != nil {
		return control.DispatchResult{}, e
	}
	return (&Dispatcher{Coordinator: c, Local: m.Local}).Send(ctx, step, id, deadline)
}
