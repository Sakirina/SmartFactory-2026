package command

import (
	"context"
	"errors"
	"testing"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
)

type fakeResolver struct {
	executor Executor
	ok       bool
}

func (r fakeResolver) ResolveDevice(string) (Executor, bool) {
	return r.executor, r.ok
}

type fakeExecutor struct {
	calls int
	fn    func(context.Context, *dtv1.DeviceMessage) (*dtv1.CommandResponsePayload, error)
}

func (e *fakeExecutor) SendCommand(ctx context.Context, msg *dtv1.DeviceMessage) (*dtv1.CommandResponsePayload, error) {
	e.calls++
	if e.fn != nil {
		return e.fn(ctx, msg)
	}
	return &dtv1.CommandResponsePayload{
		CommandId: msg.GetCommandId(),
		Status:    dtv1.CommandStatus_SUCCESS,
		Message:   "ok",
	}, nil
}

func TestRouterExecutesAndDeduplicatesCommandID(t *testing.T) {
	executor := &fakeExecutor{}
	service := NewService(time.Minute)
	service.SetResolver(fakeResolver{executor: executor, ok: true})

	result, err := service.Handle(context.Background(), testControlCommand("cmd-1"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if result.Response.GetStatus() != dtv1.CommandStatus_SUCCESS {
		t.Fatalf("status = %s, want SUCCESS", result.Response.GetStatus())
	}

	duplicate, err := service.Handle(context.Background(), testControlCommand("cmd-1"))
	if err != nil {
		t.Fatalf("duplicate Handle returned error: %v", err)
	}
	if !duplicate.Duplicate {
		t.Fatal("duplicate command_id should be marked duplicate")
	}
	if duplicate.Response.GetStatus() != dtv1.CommandStatus_REJECTED {
		t.Fatalf("duplicate status = %s, want REJECTED", duplicate.Response.GetStatus())
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
}

func TestRouterReturnsTimeout(t *testing.T) {
	executor := &fakeExecutor{
		fn: func(ctx context.Context, _ *dtv1.DeviceMessage) (*dtv1.CommandResponsePayload, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	service := NewService(time.Minute)
	service.SetResolver(fakeResolver{executor: executor, ok: true})

	cmd := testControlCommand("cmd-timeout")
	cmd.GetControl().Options = &dtv1.CommandOptions{TimeoutMs: 1}
	result, err := service.Handle(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if result.Response.GetStatus() != dtv1.CommandStatus_TIMEOUT {
		t.Fatalf("status = %s, want TIMEOUT", result.Response.GetStatus())
	}
}

func TestRouterAsyncPublishesCompletion(t *testing.T) {
	service := NewService(time.Minute)
	service.SetResolver(fakeResolver{executor: &fakeExecutor{}, ok: true})
	done := make(chan *dtv1.CommandResponsePayload, 1)

	accepted, err := service.HandleAsync(testControlCommand("cmd-async"), func(response *dtv1.CommandResponsePayload) {
		done <- response
	})
	if err != nil {
		t.Fatalf("HandleAsync returned error: %v", err)
	}
	if accepted.GetCommandId() != "cmd-async" {
		t.Fatalf("accepted command id = %q", accepted.GetCommandId())
	}

	select {
	case response := <-done:
		if response.GetStatus() != dtv1.CommandStatus_SUCCESS {
			t.Fatalf("async status = %s, want SUCCESS", response.GetStatus())
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async response")
	}
}

func TestRouterRejectsInvalidPayload(t *testing.T) {
	service := NewService(time.Minute)
	_, err := service.Handle(context.Background(), &dtv1.DeviceMessage{
		Direction: dtv1.Direction_DOWNSTREAM,
		Device:    &dtv1.DeviceIdentity{DeviceId: "device-1"},
		Type:      dtv1.MessageType_CONTROL,
		CommandId: "cmd-invalid",
	})
	if !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("error = %v, want ErrInvalidCommand", err)
	}
}

func testControlCommand(commandID string) *dtv1.DeviceMessage {
	return &dtv1.DeviceMessage{
		Direction: dtv1.Direction_DOWNSTREAM,
		Device:    &dtv1.DeviceIdentity{DeviceId: "device-1"},
		Type:      dtv1.MessageType_CONTROL,
		CommandId: commandID,
		Payload: &dtv1.DeviceMessage_Control{
			Control: &dtv1.ControlPayload{Action: "start"},
		},
	}
}
