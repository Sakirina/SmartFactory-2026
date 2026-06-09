package grpc

import (
	"context"
	"errors"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/command"
	dtruntime "competition2026/product/datatransfer/internal/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Server struct {
	dtv1.UnimplementedDataTransferServiceServer
	rt *dtruntime.Runtime
}

func Register(server grpc.ServiceRegistrar, rt *dtruntime.Runtime) {
	dtv1.RegisterDataTransferServiceServer(server, &Server{rt: rt})
}

func New(rt *dtruntime.Runtime) *Server {
	return &Server{rt: rt}
}

func (s *Server) SubscribeTelemetry(req *dtv1.SubscribeRequest, stream dtv1.DataTransferService_SubscribeTelemetryServer) error {
	filter := dtruntime.FilterFromSubscribeRequest(req, []dtv1.MessageType{dtv1.MessageType_TELEMETRY})
	return s.stream(stream.Context(), filter, stream.Send)
}

func (s *Server) SubscribeEvents(req *dtv1.SubscribeRequest, stream dtv1.DataTransferService_SubscribeEventsServer) error {
	filter := dtruntime.FilterFromSubscribeRequest(req, []dtv1.MessageType{
		dtv1.MessageType_STATUS,
		dtv1.MessageType_EVENT,
		dtv1.MessageType_CMD_RESPONSE,
	})
	return s.stream(stream.Context(), filter, stream.Send)
}

func (s *Server) PullMessages(_ context.Context, req *dtv1.PullRequest) (*dtv1.DeviceMessageBatch, error) {
	return s.rt.Pull(req), nil
}

func (s *Server) SendCommand(ctx context.Context, msg *dtv1.DeviceMessage) (*dtv1.CommandResponsePayload, error) {
	response, _, err := s.rt.HandleCommand(ctx, msg)
	if err != nil {
		return nil, grpcError(err)
	}
	return response, nil
}

func (s *Server) SendCommandAsync(ctx context.Context, msg *dtv1.DeviceMessage) (*dtv1.CommandAccepted, error) {
	accepted, err := s.rt.AcceptCommandAsync(ctx, msg)
	if err != nil {
		return nil, grpcError(err)
	}
	return accepted, nil
}

func (s *Server) PushDeviceConfig(_ context.Context, update *dtv1.DeviceConfigUpdate) (*dtv1.ConfigUpdateResponse, error) {
	return s.rt.ApplyConfig(update), nil
}

func (s *Server) ListDevices(_ context.Context, req *dtv1.ListDevicesRequest) (*dtv1.ListDevicesResponse, error) {
	return s.rt.ListDevices(req), nil
}

func (s *Server) GetMetrics(context.Context, *dtv1.MetricsRequest) (*dtv1.MetricsResponse, error) {
	return s.rt.MetricsResponse(), nil
}

func (s *Server) stream(ctx context.Context, filter dtruntime.Filter, send func(*dtv1.DeviceMessage) error) error {
	ch, cancel := s.rt.Subscribe(filter)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			if err := send(msg); err != nil {
				return err
			}
		}
	}
}

func grpcError(err error) error {
	if errors.Is(err, command.ErrInvalidCommand) {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	if dtruntime.IsDuplicateCommand(err) {
		return status.Error(codes.AlreadyExists, err.Error())
	}
	return status.Error(codes.Internal, err.Error())
}
