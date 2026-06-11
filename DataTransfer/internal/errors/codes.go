// Package errors 定义与《数据传递微服务接口文档》5.2 错误码清单对齐的错误码常量。
// DT-{模块}-{序号} 中 001~005 等编号以接口文档为准;文档未覆盖的实现细节错误
// 使用 006 起的扩展编号,并已在跨服务协作备忘中登记,待回写接口文档。
package errors

const (
	// Connector(协议适配)
	CodeConnectorConnectFailed = "DT-CON-001" // Connector 连接失败
	CodeConnectorTimeout       = "DT-CON-002" // 设备通信超时
	CodeConnectorCrashed       = "DT-CON-003" // Connector 异常退出(自动重启)
	CodeConnectorOffline       = "DT-CON-004" // 设备离线
	CodeConnectorDiscovery     = "DT-CON-005" // 新设备自动发现
	CodeConnectorInvalid       = "DT-CON-006" // 扩展:Connector 配置/协议无效

	// Converter(数据转换)
	CodeConverterFailed  = "DT-CVT-001" // 数据转换失败,跳过本条
	CodeConverterRange   = "DT-CVT-002" // 值超出合理范围,标记 UNCERTAIN
	CodeConverterInvalid = "DT-CVT-003" // Converter 配置错误

	// Command(指令下发)
	CodeCommandNoRoute     = "DT-CMD-001" // 目标设备不在线/无 Connector 路由
	CodeCommandTimeout     = "DT-CMD-002" // 指令执行超时
	CodeCommandUnsupported = "DT-CMD-003" // 指令转换失败/协议能力不足
	CodeCommandRetrying    = "DT-CMD-004" // 指令重试中
	CodeCommandDuplicate   = "DT-CMD-005" // commandId 重复,指令被拒绝
	CodeCommandInvalid     = "DT-CMD-006" // 扩展:指令格式/参数无效

	// Buffer(本地缓冲)
	CodeBufferHighWater   = "DT-BUF-001" // 缓冲容量达到 80%
	CodeBufferFull        = "DT-BUF-002" // 缓冲已满,开始淘汰旧数据
	CodeBufferReplayStart = "DT-BUF-003" // 断线续传开始
	CodeBufferReplayDone  = "DT-BUF-004" // 断线续传完成

	// Config(配置管理)
	CodeConfigApplied  = "DT-CFG-001" // 配置热加载成功
	CodeConfigRejected = "DT-CFG-002" // 配置热加载失败,保持旧配置
	CodeConfigWarning  = "DT-CFG-003" // 配置校验警告

	// Northbound(北向接口)
	CodeNorthboundDown   = "DT-NBI-001" // 北向连接断开
	CodeNorthboundUp     = "DT-NBI-002" // 北向连接恢复
	CodeNorthboundDecode = "DT-NBI-003" // 扩展:北向消息解码失败

	// Strategy(上报策略与背压)
	CodeStrategyApplied = "DT-STR-001" // 上报策略变更生效
	CodeBackpressureOn  = "DT-STR-002" // 背压触发
	CodeBackpressureOff = "DT-STR-003" // 背压解除

	// Runtime(扩展:运行时入参)
	CodeRuntimeInvalid = "DT-RUN-001"
)
