package sidecar

import (
	"context"
	"sync"
	"time"

	pluginv1 "competition2026/product/datatransfer/gen/datatransfer/plugin/v1"
	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"google.golang.org/grpc"
)

type MockServer struct {
	pluginv1.UnimplementedSidecarConnectorServiceServer

	mu          sync.Mutex
	connectorID string
	protocol    string
	state       string
	deviceCount int32
	upstream    chan *dtv1.DeviceMessage
}

func NewMockServer() *MockServer {
	return &MockServer{
		state:    "stopped",
		upstream: make(chan *dtv1.DeviceMessage, 16),
	}
}

func RegisterMock(server grpc.ServiceRegistrar, mock *MockServer) {
	pluginv1.RegisterSidecarConnectorServiceServer(server, mock)
}

func (s *MockServer) Emit(msg *dtv1.DeviceMessage) {
	s.upstream <- msg
}

func (s *MockServer) Init(_ context.Context, req *pluginv1.InitRequest) (*pluginv1.InitResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connectorID = req.GetConnectorId()
	s.protocol = req.GetProtocol()
	s.state = "initializing"
	return &pluginv1.InitResponse{Success: true}, nil
}

func (s *MockServer) Start(req *pluginv1.StartRequest, stream grpc.ServerStreamingServer[dtv1.DeviceMessage]) error {
	s.mu.Lock()
	s.connectorID = req.GetConnectorId()
	s.state = "running"
	s.mu.Unlock()
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case msg := <-s.upstream:
			if err := stream.Send(msg); err != nil {
				return err
			}
		}
	}
}

func (s *MockServer) SendCommand(_ context.Context, req *pluginv1.CommandRequest) (*dtv1.CommandResponsePayload, error) {
	return &dtv1.CommandResponsePayload{
		CommandId: req.GetCommand().GetCommandId(),
		Status:    dtv1.CommandStatus_SUCCESS,
		Message:   "sidecar mock command accepted",
		Result:    map[string]string{"connector": s.connectorID},
	}, nil
}

func (s *MockServer) ReloadConfig(_ context.Context, req *pluginv1.ReloadConfigRequest) (*pluginv1.InitResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connectorID = req.GetConnectorId()
	s.state = "running"
	return &pluginv1.InitResponse{Success: true}, nil
}

func (s *MockServer) GetStatus(context.Context, *pluginv1.StatusRequest) (*pluginv1.ConnectorStatusResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &pluginv1.ConnectorStatusResponse{
		ConnectorId:  s.connectorID,
		Protocol:     s.protocol,
		State:        s.state,
		DeviceCount:  s.deviceCount,
		ErrorMessage: "",
		Uptime:       int64(time.Second.Seconds()),
	}, nil
}

func (s *MockServer) Stop(context.Context, *pluginv1.StopRequest) (*pluginv1.EmptyResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = "stopped"
	return &pluginv1.EmptyResponse{}, nil
}
