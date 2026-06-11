// Package strategy 实现可选的上报策略引擎(FR-S-033 ~ FR-S-036)。
//
// 策略仅作用于 TELEMETRY 消息;STATUS、EVENT、CMD_RESPONSE 永远即时放行。
// 配置三级覆盖:数据点级 > Connector 级 > 全局默认(FR-S-035)。
// 默认(未配置或 ON_RECEIVED)等价于关闭过滤,所有数据即采即传。
//
// 周期类模式采用惰性求值:数据点到达时检查距上次交付是否已满一个周期,
// 满则交付,否则过滤。设备停止上报时不会凭空重发旧值;该语义已与
// 软件设计 4.6 对齐并记录于 docs。策略参数变化时,旧状态自动失效重建。
package strategy

import (
	"math"
	"sync"
	"sync/atomic"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	"google.golang.org/protobuf/proto"
)

const (
	ModeUnspecified      = "STRATEGY_UNSPECIFIED"
	ModeOnReceived       = "ON_RECEIVED"
	ModeOnChange         = "ON_CHANGE"
	ModeOnReportPeriod   = "ON_REPORT_PERIOD"
	ModeOnChangeOrPeriod = "ON_CHANGE_OR_REPORT_PERIOD"

	stateTTL        = time.Hour
	pruneEveryCalls = 1024
)

// Resolver 提供数据点级与 Connector 级策略查询,由 Connector Manager 实现。
type Resolver interface {
	StrategyFor(connectorID, deviceID, key string) (datapoint *config.ReportStrategyConfig, connectorLevel *config.ReportStrategyConfig)
}

type Engine struct {
	mu       sync.Mutex
	global   config.ReportStrategyConfig
	resolver Resolver
	states   map[stateKey]*datapointState
	calls    int64

	filteredMessages  atomic.Int64 // 整条消息被过滤数(MetricsResponse.strategy_filtered_count)
	deliveredMessages atomic.Int64
	filteredPoints    atomic.Int64 // 数据点粒度过滤数(日志/调试用)
}

type stateKey struct {
	connectorID string
	deviceID    string
	key         string
}

type datapointState struct {
	fingerprint string // 策略参数指纹;策略变更后旧状态自动失效(设计 4.6)
	hasLast     bool
	lastKind    byte
	lastNumber  float64
	lastText    string
	lastBool    bool
	lastDeliver time.Time
	lastTouch   time.Time
}

func NewEngine(global config.ReportStrategyConfig) *Engine {
	return &Engine{
		global: global,
		states: make(map[stateKey]*datapointState),
	}
}

func (e *Engine) SetResolver(resolver Resolver) {
	e.mu.Lock()
	e.resolver = resolver
	e.mu.Unlock()
}

// SetGlobal 热更新全局默认策略(UPDATE_GLOBAL / DT-STR-001)。
func (e *Engine) SetGlobal(global config.ReportStrategyConfig) {
	e.mu.Lock()
	e.global = global
	e.mu.Unlock()
}

func (e *Engine) Global() config.ReportStrategyConfig {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.global
}

// Stats 返回 (整条过滤消息数, 已交付消息数, 数据点粒度过滤数)。
func (e *Engine) Stats() (int64, int64, int64) {
	return e.filteredMessages.Load(), e.deliveredMessages.Load(), e.filteredPoints.Load()
}

// Apply 对 TELEMETRY 消息执行策略过滤:
// 返回原消息(全部放行)、重建后的部分消息(部分放行)或 nil(整条过滤)。
// 非 TELEMETRY 消息原样返回(FR-S-036)。
func (e *Engine) Apply(msg *dtv1.DeviceMessage) *dtv1.DeviceMessage {
	if msg == nil {
		return nil
	}
	if msg.GetType() != dtv1.MessageType_TELEMETRY {
		return msg
	}
	telemetry := msg.GetTelemetry()
	datapoints := telemetry.GetDatapoints()
	if len(datapoints) == 0 {
		e.deliveredMessages.Add(1)
		return msg
	}

	device := msg.GetDevice()
	now := time.Now()
	kept := make([]*dtv1.Datapoint, 0, len(datapoints))

	e.mu.Lock()
	e.maybePruneLocked(now)
	for _, dp := range datapoints {
		strategy := e.resolveLocked(device.GetConnectorId(), device.GetDeviceId(), dp.GetKey())
		if e.allowLocked(device, dp, strategy, now) {
			kept = append(kept, dp)
		}
	}
	e.mu.Unlock()

	dropped := len(datapoints) - len(kept)
	if dropped > 0 {
		e.filteredPoints.Add(int64(dropped))
	}
	if len(kept) == 0 {
		e.filteredMessages.Add(1)
		return nil
	}
	e.deliveredMessages.Add(1)
	if dropped == 0 {
		return msg
	}
	out := proto.Clone(msg).(*dtv1.DeviceMessage)
	out.Payload = &dtv1.DeviceMessage_Telemetry{Telemetry: &dtv1.TelemetryPayload{Datapoints: kept}}
	return out
}

func (e *Engine) resolveLocked(connectorID, deviceID, key string) config.ReportStrategyConfig {
	if e.resolver != nil {
		dpLevel, connLevel := e.resolver.StrategyFor(connectorID, deviceID, key)
		if dpLevel != nil && configured(*dpLevel) {
			return *dpLevel
		}
		if connLevel != nil && configured(*connLevel) {
			return *connLevel
		}
	}
	return e.global
}

func configured(strategy config.ReportStrategyConfig) bool {
	return strategy.Mode != "" && strategy.Mode != ModeUnspecified
}

func (e *Engine) allowLocked(device *dtv1.DeviceIdentity, dp *dtv1.Datapoint, strategy config.ReportStrategyConfig, now time.Time) bool {
	mode := strategy.Mode
	if mode == "" || mode == ModeUnspecified || mode == ModeOnReceived {
		return true
	}

	key := stateKey{connectorID: device.GetConnectorId(), deviceID: device.GetDeviceId(), key: dp.GetKey()}
	fingerprint := fingerprintOf(strategy)
	state, ok := e.states[key]
	if !ok || state.fingerprint != fingerprint {
		state = &datapointState{fingerprint: fingerprint}
		e.states[key] = state
	}
	state.lastTouch = now

	changed := !state.hasLast || valueChanged(state, dp.GetValue(), strategy.Deadband)
	period := time.Duration(strategy.PeriodSeconds) * time.Second
	periodElapsed := period > 0 && (state.lastDeliver.IsZero() || now.Sub(state.lastDeliver) >= period)

	allow := false
	switch mode {
	case ModeOnChange:
		allow = changed
	case ModeOnReportPeriod:
		allow = periodElapsed
	case ModeOnChangeOrPeriod:
		allow = changed || periodElapsed
	default:
		allow = true
	}
	if allow {
		state.lastDeliver = now
		rememberValue(state, dp.GetValue())
	}
	return allow
}

func valueChanged(state *datapointState, value *dtv1.DataValue, deadband float64) bool {
	kind, number, text, boolean := classify(value)
	if kind != state.lastKind {
		return true
	}
	switch kind {
	case 'n':
		if deadband > 0 {
			return math.Abs(number-state.lastNumber) > deadband
		}
		return number != state.lastNumber
	case 'b':
		return boolean != state.lastBool
	default:
		return text != state.lastText
	}
}

func rememberValue(state *datapointState, value *dtv1.DataValue) {
	state.hasLast = true
	state.lastKind, state.lastNumber, state.lastText, state.lastBool = classify(value)
}

// classify 将 DataValue 归一为可比较表示:'n' 数值、'b' 布尔、's' 文本/字节、0 空。
func classify(value *dtv1.DataValue) (byte, float64, string, bool) {
	switch typed := value.GetKind().(type) {
	case *dtv1.DataValue_DoubleValue:
		return 'n', typed.DoubleValue, "", false
	case *dtv1.DataValue_IntValue:
		return 'n', float64(typed.IntValue), "", false
	case *dtv1.DataValue_BoolValue:
		return 'b', 0, "", typed.BoolValue
	case *dtv1.DataValue_StringValue:
		return 's', 0, typed.StringValue, false
	case *dtv1.DataValue_BytesValue:
		return 's', 0, string(typed.BytesValue), false
	default:
		return 0, 0, "", false
	}
}

func fingerprintOf(strategy config.ReportStrategyConfig) string {
	// 模式 + 周期 + 死区共同构成指纹;任一变化都会重建数据点状态。
	return strategy.Mode + "|" + itoa(strategy.PeriodSeconds) + "|" + ftoa(strategy.Deadband)
}

func (e *Engine) maybePruneLocked(now time.Time) {
	e.calls++
	if e.calls%pruneEveryCalls != 0 {
		return
	}
	for key, state := range e.states {
		if now.Sub(state.lastTouch) > stateTTL {
			delete(e.states, key)
		}
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var buf [20]byte
	idx := len(buf)
	for value > 0 {
		idx--
		buf[idx] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		idx--
		buf[idx] = '-'
	}
	return string(buf[idx:])
}

func ftoa(value float64) string {
	bits := math.Float64bits(value)
	var buf [16]byte
	const hex = "0123456789abcdef"
	for i := 0; i < 16; i++ {
		buf[15-i] = hex[bits&0xf]
		bits >>= 4
	}
	return string(buf[:])
}
