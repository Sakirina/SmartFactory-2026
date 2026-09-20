// Package command 实现 Command Router 与 Ack Tracker(设计 4.9):
// 校验下行指令、按 command_id 去重(DT-CMD-005)、路由至 Connector 执行,
// 支持按 CommandOptions 的超时与重试(FR-S-014),维护指令结果的 TTL 记录。
package command

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	dterrors "competition2026/product/datatransfer/internal/errors"
	"competition2026/product/datatransfer/internal/state"
	"google.golang.org/protobuf/proto"
)

var (
	ErrInvalidCommand = errors.New("invalid command")
	ErrDuplicate      = errors.New("duplicate command")
)

// 按指令类型的默认超时(FR-S-014:超时时间按指令类型可配置,CONTROL 默认较短)。
// CommandOptions.timeout_ms > 0 时覆盖默认值。
const (
	defaultControlTimeout = 10 * time.Second
	defaultCommandTimeout = 30 * time.Second
)

// RetryPolicy 为重试间隔策略(FR-S-014:固定间隔与指数退避均须支持)。
// 重试次数不在此处:始终由调用方按指令通过 CommandOptions.retry_count 指定。
type RetryPolicy struct {
	Mode        string // RetryModeFixed | RetryModeExponential
	Interval    time.Duration
	MaxInterval time.Duration
}

const (
	RetryModeFixed       = "fixed"
	RetryModeExponential = "exponential"
)

// DefaultRetryPolicy 与历史行为一致:指数退避,200ms 起、上限 5s。
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		Mode:        RetryModeExponential,
		Interval:    200 * time.Millisecond,
		MaxInterval: 5 * time.Second,
	}
}

// intervalFor 计算第 attempt 次重试(从 1 起)前的等待时长。
func (p RetryPolicy) intervalFor(attempt int) time.Duration {
	interval := p.Interval
	if interval <= 0 {
		interval = DefaultRetryPolicy().Interval
	}
	if p.Mode == RetryModeFixed {
		return interval
	}
	limit := p.MaxInterval
	if limit <= 0 {
		limit = DefaultRetryPolicy().MaxInterval
	}
	backoff := interval << (attempt - 1)
	if backoff > limit || backoff <= 0 {
		return limit
	}
	return backoff
}

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
	retry    RetryPolicy
	resolver Resolver
	records  map[string]record
	journal  *state.Store
	ctx      context.Context
	cancel   context.CancelFunc
	closed   bool
	wg       sync.WaitGroup
}

type record struct {
	response  *dtv1.CommandResponsePayload
	status    string
	request   *dtv1.DeviceMessage
	expiresAt time.Time
}

func NewService(ttl time.Duration) *Service {
	return NewServiceWithRetry(ttl, DefaultRetryPolicy())
}

func NewServiceWithRetry(ttl time.Duration, retry RetryPolicy) *Service {
	if ttl <= 0 {
		ttl = time.Hour
	}
	if retry.Mode != RetryModeFixed && retry.Mode != RetryModeExponential {
		retry = DefaultRetryPolicy()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		ctx: ctx, cancel: cancel,
		ttl:     ttl,
		retry:   retry,
		records: make(map[string]record),
	}
}

func (s *Service) SetJournal(journal *state.Store) { s.mu.Lock(); s.journal = journal; s.mu.Unlock() }

func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
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
	msg = proto.Clone(msg).(*dtv1.DeviceMessage)
	duplicate, response, err := s.reserve(ctx, msg, "running")
	if err != nil {
		return Result{}, err
	}
	if duplicate {
		return Result{Response: response, Duplicate: true}, nil
	}

	execCtx, cancel := withCommandTimeout(ctx, msg)
	defer cancel()
	response = s.execute(execCtx, msg)
	if err := s.complete(msg.CommandId, response); err != nil {
		return Result{}, err
	}
	return Result{Response: response}, nil
}

func (s *Service) HandleAsync(msg *dtv1.DeviceMessage, publish func(*dtv1.CommandResponsePayload)) (*dtv1.CommandAccepted, error) {
	if err := validate(msg); err != nil {
		return nil, err
	}
	msg = proto.Clone(msg).(*dtv1.DeviceMessage)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, context.Canceled
	}
	s.wg.Add(1)
	s.mu.Unlock()
	duplicate, _, err := s.reserve(s.ctx, msg, "accepted")
	if err != nil {
		s.wg.Done()
		return nil, err
	}
	if duplicate {
		s.wg.Done()
		return nil, ErrDuplicate
	}
	acceptedAt := time.Now()
	go func() {
		defer s.wg.Done()
		ctx, cancel := withCommandTimeout(s.ctx, msg)
		defer cancel()
		response := s.execute(ctx, msg)
		if err := s.complete(msg.CommandId, response); err != nil {
			response = &dtv1.CommandResponsePayload{CommandId: msg.CommandId, Status: dtv1.CommandStatus_RESULT_UNKNOWN, Message: "cannot persist command outcome: " + err.Error()}
		}
		if publish != nil {
			publish(response)
		}
	}()
	return &dtv1.CommandAccepted{
		CommandId:  msg.CommandId,
		AcceptedAt: acceptedAt.UnixMilli(),
	}, nil
}

func (s *Service) reserve(ctx context.Context, msg *dtv1.DeviceMessage, internalStatus string) (bool, *dtv1.CommandResponsePayload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false, nil, context.Canceled
	}
	if s.journal != nil {
		response, duplicate, err := s.journal.Reserve(ctx, msg)
		return duplicate, response, err
	}
	s.prune(time.Now())
	if existing, ok := s.records[msg.CommandId]; ok {
		if !proto.Equal(existing.request, msg) {
			return false, nil, state.ErrConflict
		}
		return true, proto.Clone(existing.response).(*dtv1.CommandResponsePayload), nil
	}
	s.records[msg.CommandId] = record{request: proto.Clone(msg).(*dtv1.DeviceMessage), response: &dtv1.CommandResponsePayload{CommandId: msg.CommandId, Status: dtv1.CommandStatus_RESULT_UNKNOWN, Message: internalStatus}, status: internalStatus, expiresAt: time.Now().Add(s.ttl)}
	return false, nil, nil
}

// execute 下发指令并按 CommandOptions.retry_count 重试(FR-S-014)。
// 查询和明确声明幂等的控制可重试;其他控制的传输错误进入结果待核对状态。
// 重试间隔由服务级 RetryPolicy 决定(固定间隔或指数退避)。
func (s *Service) execute(ctx context.Context, msg *dtv1.DeviceMessage) *dtv1.CommandResponsePayload {
	executor, ok := s.resolve(msg.GetDevice().GetDeviceId())
	if !ok {
		return rejected(msg.CommandId, dterrors.CodeCommandNoRoute, "target device has no connector route")
	}
	if deadline := msg.GetControl().GetOptions().GetStartDeadlineMs(); deadline > 0 && time.Now().UnixMilli() >= deadline {
		return rejected(msg.CommandId, dterrors.CodeCommandTimeout, "command start deadline expired")
	}
	retries := commandRetryCount(msg)
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			slog.Warn("command retrying",
				"code", dterrors.CodeCommandRetrying,
				"command_id", msg.CommandId,
				"device_id", msg.GetDevice().GetDeviceId(),
				"attempt", attempt,
				"max_retries", retries,
			)
			if !sleepContext(ctx, s.retry.intervalFor(attempt)) {
				break
			}
		}
		if err := ctx.Err(); err != nil {
			lastErr = err
			break
		}
		if attempt == 0 {
			if deadline := msg.GetControl().GetOptions().GetStartDeadlineMs(); deadline > 0 && time.Now().UnixMilli() >= deadline {
				return rejected(msg.CommandId, dterrors.CodeCommandTimeout, "command start deadline expired")
			}
		}
		response, err := executor.SendCommand(ctx, msg)
		if err == nil {
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
		if !commandIsIdempotent(msg) {
			return &dtv1.CommandResponsePayload{CommandId: msg.CommandId, Status: dtv1.CommandStatus_RESULT_UNKNOWN, Message: "device outcome must be reconciled: " + err.Error()}
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &dtv1.CommandResponsePayload{
			CommandId: msg.CommandId,
			Status:    dtv1.CommandStatus_TIMEOUT,
			Message:   fmt.Sprintf("%s: command timed out", dterrors.CodeCommandTimeout),
		}
	}
	message := "command failed"
	if lastErr != nil {
		message = lastErr.Error()
	}
	return &dtv1.CommandResponsePayload{
		CommandId: msg.CommandId,
		Status:    dtv1.CommandStatus_FAILURE,
		Message:   message,
	}
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

func (s *Service) complete(commandID string, response *dtv1.CommandResponsePayload) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.journal != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.journal.Complete(ctx, response)
	}
	entry := s.records[commandID]
	entry.response = proto.Clone(response).(*dtv1.CommandResponsePayload)
	entry.status = response.Status.String()
	entry.expiresAt = time.Now().Add(s.ttl)
	s.records[commandID] = entry
	return nil
}

func commandIsIdempotent(msg *dtv1.DeviceMessage) bool {
	return msg.Type == dtv1.MessageType_QUERY || msg.GetControl().GetOptions().GetIdempotent()
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
		if msg.GetType() == dtv1.MessageType_CONTROL {
			timeout = defaultControlTimeout
		} else {
			timeout = defaultCommandTimeout
		}
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

// commandRetryCount 取 CommandOptions.retry_count;0 表示不重试(与 proto 注释一致)。
func commandRetryCount(msg *dtv1.DeviceMessage) int {
	var count int32
	switch payload := msg.GetPayload().(type) {
	case *dtv1.DeviceMessage_Control:
		count = payload.Control.GetOptions().GetRetryCount()
	case *dtv1.DeviceMessage_Query:
		count = payload.Query.GetOptions().GetRetryCount()
	}
	if count < 0 || !commandIsIdempotent(msg) {
		return 0
	}
	return int(count)
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
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
		if now.After(entry.expiresAt) && entry.status != "running" && entry.status != "accepted" {
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
