package sidecar

import (
	plugin "competition2026/product/datatransfer/gen/datatransfer/plugin/v1"
	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	"competition2026/product/datatransfer/internal/connector"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type Process struct {
	mu     sync.RWMutex
	cfg    config.ConnectorConfig
	status connector.Status
	cancel context.CancelFunc
	client plugin.SidecarConnectorServiceClient
}

func init() { connector.Register("process", func() connector.Connector { return &Process{} }) }
func (p *Process) Init(cfg config.ConnectorConfig) error {
	if cfg.Process == nil || !filepath.IsAbs(cfg.Process.Executable) {
		return errors.New("process connector requires an absolute executable path")
	}
	info, e := os.Stat(cfg.Process.Executable)
	if e != nil {
		return e
	}
	if info.IsDir() || info.Mode()&0111 == 0 {
		return errors.New("plugin executable is not executable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg = cfg
	p.status = connector.NewStatus(cfg.ConnectorID, cfg.Protocol)
	p.status.DeviceCount = len(cfg.Devices)
	return nil
}
func (p *Process) Start(parent context.Context, upstream chan<- *dt.DeviceMessage) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	p.mu.Lock()
	cfg := p.cfg
	p.cancel = cancel
	p.mu.Unlock()
	dir, e := os.MkdirTemp("", "sf-plugin-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "connector.sock")
	command := exec.CommandContext(ctx, cfg.Process.Executable, cfg.Process.Args...)
	command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "SF_PLUGIN_SOCKET=" + socket}
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if e = command.Start(); e != nil {
		return e
	}
	exited := make(chan error, 1)
	go func() { err := command.Wait(); cancel(); exited <- err }()
	defer func() { cancel(); <-exited }()
	conn, e := grpc.NewClient("unix:"+socket, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(2<<20)))
	if e != nil {
		return e
	}
	defer conn.Close()
	client := plugin.NewSidecarConnectorServiceClient(conn)
	configJSON, e := json.Marshal(cfg)
	if e != nil {
		return e
	}
	ready, done := context.WithTimeout(ctx, 5*time.Second)
	defer done()
	result, e := client.Init(ready, &plugin.InitRequest{ConnectorId: cfg.ConnectorID, Protocol: cfg.Protocol, ConfigJson: configJSON}, grpc.WaitForReady(true))
	if e != nil {
		return fmt.Errorf("plugin initialization: %w", e)
	}
	if !result.Success {
		return errors.New(result.ErrorMessage)
	}
	p.mu.Lock()
	p.client = client
	p.status.State = connector.StateRunning
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.client = nil; p.status.State = connector.StateStopped; p.mu.Unlock() }()
	stream, e := client.Start(ctx, &plugin.StartRequest{ConnectorId: cfg.ConnectorID})
	if e != nil {
		return e
	}
	for {
		message, e := stream.Recv()
		if e != nil {
			if parent.Err() != nil {
				return nil
			}
			return fmt.Errorf("plugin stream ended: %w", e)
		}
		if message.GetDevice().GetConnectorId() != cfg.ConnectorID {
			return errors.New("plugin emitted an invalid connector identity")
		}
		found := false
		for _, device := range cfg.Devices {
			if device.DeviceID == message.GetDevice().GetDeviceId() {
				found = true
				break
			}
		}
		if !found {
			return errors.New("plugin emitted an unconfigured device")
		}
		select {
		case upstream <- message:
			p.mu.Lock()
			p.status.Stats.MessagesIn++
			p.mu.Unlock()
		case <-ctx.Done():
			if parent.Err() != nil {
				return nil
			}
			return errors.New("plugin process exited while forwarding data")
		}
	}
}
func (p *Process) Stop() error {
	p.mu.RLock()
	cancel := p.cancel
	p.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
	return nil
}
func (p *Process) Status() connector.Status { p.mu.RLock(); defer p.mu.RUnlock(); return p.status }
func (p *Process) Devices() []*dt.DeviceInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := []*dt.DeviceInfo{}
	for _, d := range p.cfg.Devices {
		out = append(out, connector.DeviceInfoFromConfig(p.cfg.ConnectorID, p.cfg.Protocol, d.Tags, d, dt.DeviceState_UNKNOWN, time.Now()))
	}
	return out
}
func (p *Process) ReloadConfig(cfg config.ConnectorConfig) error { return p.Init(cfg) }
func (p *Process) SendCommand(ctx context.Context, command *dt.DeviceMessage) (*dt.CommandResponsePayload, error) {
	p.mu.RLock()
	client := p.client
	p.mu.RUnlock()
	if client == nil {
		return nil, errors.New("plugin is disconnected")
	}
	return client.SendCommand(ctx, &plugin.CommandRequest{Command: command})
}
