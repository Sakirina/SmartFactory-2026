// Package plugin provides the process-isolated connector protocol. Implementations
// exchange generated DeviceMessage contracts over a private Unix-domain socket.
package plugin

import (
	wire "competition2026/product/datatransfer/gen/datatransfer/plugin/v1"
	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"context"
	"encoding/json"
	"errors"
	"google.golang.org/grpc"
	"net"
	"os"
	"sync"
)

type Connector interface {
	Init(json.RawMessage) error
	Run(context.Context, func(*dt.DeviceMessage) error) error
	Command(context.Context, *dt.DeviceMessage) (*dt.CommandResponsePayload, error)
}
type service struct {
	wire.UnimplementedSidecarConnectorServiceServer
	implementation      Connector
	mu                  sync.Mutex
	id, protocol, state string
	cancel              context.CancelFunc
}

func (s *service) Init(_ context.Context, request *wire.InitRequest) (*wire.InitResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.implementation.Init(request.ConfigJson); e != nil {
		return &wire.InitResponse{ErrorMessage: e.Error()}, nil
	}
	s.id, s.protocol, s.state = request.ConnectorId, request.Protocol, "initialized"
	return &wire.InitResponse{Success: true}, nil
}
func (s *service) Start(_ *wire.StartRequest, stream grpc.ServerStreamingServer[dt.DeviceMessage]) error {
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return errors.New("connector is already streaming")
	}
	s.cancel = cancel
	s.state = "running"
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.cancel = nil; s.state = "stopped"; s.mu.Unlock() }()
	return s.implementation.Run(ctx, stream.Send)
}
func (s *service) SendCommand(ctx context.Context, request *wire.CommandRequest) (*dt.CommandResponsePayload, error) {
	return s.implementation.Command(ctx, request.Command)
}
func (s *service) Stop(context.Context, *wire.StopRequest) (*wire.EmptyResponse, error) {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return &wire.EmptyResponse{}, nil
}
func (s *service) GetStatus(context.Context, *wire.StatusRequest) (*wire.ConnectorStatusResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &wire.ConnectorStatusResponse{ConnectorId: s.id, Protocol: s.protocol, State: s.state}, nil
}
func Serve(ctx context.Context, implementation Connector) error {
	socket := os.Getenv("SF_PLUGIN_SOCKET")
	if socket == "" {
		return errors.New("SF_PLUGIN_SOCKET required")
	}
	listener, e := net.Listen("unix", socket)
	if e != nil {
		return e
	}
	defer listener.Close()
	defer os.Remove(socket)
	if e = os.Chmod(socket, 0600); e != nil {
		return e
	}
	server := grpc.NewServer(grpc.MaxRecvMsgSize(2 << 20))
	wire.RegisterSidecarConnectorServiceServer(server, &service{implementation: implementation})
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		server.Stop()
		<-done
		return nil
	case e := <-done:
		return e
	}
}
