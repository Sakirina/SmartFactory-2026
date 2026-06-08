package command

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	dterrors "competition2026/product/datatransfer/internal/errors"
)

var (
	ErrInvalidCommand = errors.New("invalid command")
	ErrDuplicate      = errors.New("duplicate command")
)

type Executor interface {
	SendCommand(ctx context.Context, cmd *dtv1.DeviceMessage) (*dtv1.CommandResponsePayload, error)
}

type Resolver interface {
	ResolveDevice(deviceID string) (Executor, bool)
}

type Result struct {
	Response  *dtv1.CommandResponsePayload
	Duplicate bool
}

type Service struct {
	mu       sync.Mutex
	ttl      time.Duration
	resolver Resolver
	records  map[string]record
}

type record struct {
	response  *dtv1.CommandResponsePayload
	status    string
	expiresAt time.Time
}

func NewService(ttl time.Duration) *Service {
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &Service{
		ttl:     ttl,
		records: make(map[string]record),
	}
}

func (s *Service) SetResolver(resolver Resolver) {
	s.mu.Lock()
	s.resolver = resolver
	s.mu.Unlock()
}

func (s *Service) Handle(ctx context.Context, msg *dtv1.DeviceMessage) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := validate(msg); err != nil {
		return Result{}, err
	}
	if duplicate, response := s.reserve(msg.CommandId, "running"); duplicate {
		return Result{Response: response, Duplicate: true}, nil
	}

	execCtx, cancel := withCommandTimeout(ctx, msg)
	defer cancel()
	response := s.execute(execCtx, msg)
	s.complete(msg.CommandId, response)
	return Result{Response: response}, nil
}

func (s *Service) HandleAsync(msg *dtv1.DeviceMessage, publish func(*dtv1.CommandResponsePayload)) (*dtv1.CommandAccepted, error) {
	if err := validate(msg); err != nil {
		return nil, err
	}
	if duplicate, _ := s.reserve(msg.CommandId, "accepted"); duplicate {
		return nil, ErrDuplicate
	}
	acceptedAt := time.Now()
	go func() {
		ctx, cancel := withCommandTimeout(context.Background(), msg)
		defer cancel()
		response := s.execute(ctx, msg)
		s.complete(msg.CommandId, response)
		if publish != nil {
			publish(response)
		}
	}()
	return &dtv1.CommandAccepted{
		CommandId:  msg.CommandId,
		AcceptedAt: acceptedAt.UnixMilli(),
	}, nil
}

func (s *Service) reserve(commandID string, internalStatus string) (bool, *dtv1.CommandResponsePayload) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	s.prune(now)
	if existing, ok := s.records[commandID]; ok {
		return true, &dtv1.CommandResponsePayload{
			CommandId: commandID,
			Status:    dtv1.CommandStatus_REJECTED,
			Message:   fmt.Sprintf("%s: duplicate command", dterrors.CodeCommandDuplicate),
			Result:    copyMap(existing.response.GetResult()),
		}
	}
	s.records[commandID] = record{
		response: &dtv1.CommandResponsePayload{
			CommandId: commandID,
			Status:    dtv1.CommandStatus_CMD_STATUS_UNSPECIFIED,
			Message:   internalStatus,
		},
		status:    internalStatus,
		expiresAt: now.Add(s.ttl),
	}
	return false, nil
}

func (s *Service) execute(ctx context.Context, msg *dtv1.DeviceMessage) *dtv1.CommandResponsePayload {
	executor, ok := s.resolve(msg.GetDevice().GetDeviceId())
	if !ok {
		return rejected(msg.CommandId, dterrors.CodeCommandNoConnector, "target device has no connector route")
	}
	response, err := executor.SendCommand(ctx, msg)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return &dtv1.CommandResponsePayload{
				CommandId: msg.CommandId,
				Status:    dtv1.CommandStatus_TIMEOUT,
				Message:   fmt.Sprintf("%s: command timed out", dterrors.CodeCommandTimeout),
			}
		}
		return &dtv1.CommandResponsePayload{
			CommandId: msg.CommandId,
			Status:    dtv1.CommandStatus_FAILURE,
			Message:   err.Error(),
		}
	}
	if response == nil {
		return &dtv1.CommandResponsePayload{
			CommandId: msg.CommandId,
			Status:    dtv1.CommandStatus_FAILURE,
			Message:   "connector returned nil command response",
		}
	}
	if response.CommandId == "" {
		response.CommandId = msg.CommandId
	}
	return response
}

func (s *Service) resolve(deviceID string) (Executor, bool) {
	s.mu.Lock()
	resolver := s.resolver
	s.mu.Unlock()
	if resolver == nil || deviceID == "" {
		return nil, false
	}
	return resolver.ResolveDevice(deviceID)
}

func (s *Service) complete(commandID string, response *dtv1.CommandResponsePayload) {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := "failure"
	if response != nil {
		status = response.GetStatus().String()
	}
	s.records[commandID] = record{
		response:  response,
		status:    status,
		expiresAt: time.Now().Add(s.ttl),
	}
}

func validate(msg *dtv1.DeviceMessage) error {
	if msg == nil {
		return fmt.Errorf("%w: message is nil", ErrInvalidCommand)
	}
	if msg.CommandId == "" {
		return fmt.Errorf("%w: command_id is required", ErrInvalidCommand)
	}
	if msg.Direction != dtv1.Direction_DOWNSTREAM {
		return fmt.Errorf("%w: direction must be DOWNSTREAM", ErrInvalidCommand)
	}
	if msg.GetDevice().GetDeviceId() == "" {
		return fmt.Errorf("%w: device.device_id is required", ErrInvalidCommand)
	}
	switch msg.Type {
	case dtv1.MessageType_CONTROL:
		if _, ok := msg.Payload.(*dtv1.DeviceMessage_Control); !ok {
			return fmt.Errorf("%w: CONTROL requires control payload", ErrInvalidCommand)
		}
	case dtv1.MessageType_PARAM_UPDATE:
		if _, ok := msg.Payload.(*dtv1.DeviceMessage_ParamUpdate); !ok {
			return fmt.Errorf("%w: PARAM_UPDATE requires param_update payload", ErrInvalidCommand)
		}
	case dtv1.MessageType_QUERY:
		if _, ok := msg.Payload.(*dtv1.DeviceMessage_Query); !ok {
			return fmt.Errorf("%w: QUERY requires query payload", ErrInvalidCommand)
		}
	default:
		return fmt.Errorf("%w: unsupported message type %s", ErrInvalidCommand, msg.Type.String())
	}
	return nil
}

func withCommandTimeout(ctx context.Context, msg *dtv1.DeviceMessage) (context.Context, context.CancelFunc) {
	timeout := commandTimeout(msg)
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return context.WithTimeout(ctx, timeout)
}

func commandTimeout(msg *dtv1.DeviceMessage) time.Duration {
	var timeoutMS int32
	switch payload := msg.GetPayload().(type) {
	case *dtv1.DeviceMessage_Control:
		timeoutMS = payload.Control.GetOptions().GetTimeoutMs()
	case *dtv1.DeviceMessage_ParamUpdate:
		timeoutMS = 0
	case *dtv1.DeviceMessage_Query:
		timeoutMS = payload.Query.GetOptions().GetTimeoutMs()
	}
	if timeoutMS <= 0 {
		return 0
	}
	return time.Duration(timeoutMS) * time.Millisecond
}

func rejected(commandID, code, message string) *dtv1.CommandResponsePayload {
	return &dtv1.CommandResponsePayload{
		CommandId: commandID,
		Status:    dtv1.CommandStatus_REJECTED,
		Message:   fmt.Sprintf("%s: %s", code, message),
	}
}

func (s *Service) prune(now time.Time) {
	for commandID, entry := range s.records {
		if now.After(entry.expiresAt) {
			delete(s.records, commandID)
		}
	}
}

func copyMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
