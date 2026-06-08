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
	ErrNoConnectorP0  = errors.New("no southbound connector is available in P0")
)

type Result struct {
	Response  *dtv1.CommandResponsePayload
	Duplicate bool
}

type Service struct {
	mu      sync.Mutex
	ttl     time.Duration
	records map[string]record
}

type record struct {
	response  *dtv1.CommandResponsePayload
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

func (s *Service) Handle(ctx context.Context, msg *dtv1.DeviceMessage) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := validate(msg); err != nil {
		return Result{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	s.prune(now)
	if existing, ok := s.records[msg.CommandId]; ok {
		return Result{
			Response: &dtv1.CommandResponsePayload{
				CommandId: msg.CommandId,
				Status:    dtv1.CommandStatus_REJECTED,
				Message:   fmt.Sprintf("%s: duplicate command", dterrors.CodeCommandDuplicate),
				Result:    copyMap(existing.response.GetResult()),
			},
			Duplicate: true,
		}, nil
	}

	response := &dtv1.CommandResponsePayload{
		CommandId: msg.CommandId,
		Status:    dtv1.CommandStatus_REJECTED,
		Message:   fmt.Sprintf("%s: %s", dterrors.CodeCommandNoConnector, ErrNoConnectorP0.Error()),
		Result: map[string]string{
			"phase": "P0",
		},
	}
	s.records[msg.CommandId] = record{
		response:  response,
		expiresAt: now.Add(s.ttl),
	}
	return Result{Response: response}, nil
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
