# P3.5 收尾审计与修复记录

> 日期:2026-06-10
> 范围:对照 `Baseline/数据传递微服务/`(需求 v2.2、接口 v2.2、软件设计 v1.0)对 `SmartFactory-2026/DataTransfer` 全量审计,修复发现的功能/性能/安全问题,补齐基线缺口。
> 验证:`go build`、`go vet`(零告警)、`go test -race -count=1 ./...` 全部通过;真实启动 smoke(healthz/readyz/metrics)通过;示例配置加载固化为回归测试。

## 1. 基线完成度结论

| 基线域 | 结论 |
|--------|------|
| 统一数据模型(第5章)/Protobuf 契约 | ✅ 与接口文档 v2.2 一致(device 字段 4、command_id 字段 6、entity_revision 字段 5) |
| 方案A gRPC 全部 8 个方法 / 方案B MQTT topic+QoS / 管理端点 | ✅ |
| 南向三协议(Modbus TCP、MQTT Device、OPC-UA) | ✅(OPC-UA native Subscription 留 P4,DT-COL-016) |
| 指令链路 FR-S-012/013/015(去重、生命周期、回执) | ✅ |
| **指令重试 FR-S-014** | ❌→✅ 本轮补齐(此前 DT-COL-002 标记完成但重试缺失) |
| 方案B 可靠传输 FR-S-022~025(两阶段、续传、限速、TTL) | ✅ |
| 配置热加载 FR-S-026~029a(幂等、乱序保护) | ✅(并发竞态窗口本轮修复) |
| **上报策略 FR-S-033~036** | ❌→✅ 本轮实现(此前完全缺失且未登记) |
| **背压控制 FR-S-037~039** | ❌→✅ 本轮实现观测水位 + BP_BLOCK/BP_DROP_OLDEST(BP_DEGRADE 留 P4,DT-COL-015) |
| 监控 FR-S-030~032 | ⚠️→✅ 速率字段此前填累计值、/metrics 缺 per-connector 与策略/背压指标,均已修复 |
| 错误码体系(接口文档 5.2) | ❌→✅ 实现与文档此前冲突(DT-CMD-001~004 被挪用),已对齐;新增扩展码登记 CSC-005 |
| NFR-002 性能基线 | ⏳ 未压测,登记 DT-COL-014(P4) |
| NFR-008/009(插件隔离、自动恢复) | ⚠️→✅ Connector panic 此前会带崩进程、异常退出无自动重启,本轮补 recover + 指数退避重启 |
| NFR-004/012(信创、单二进制) | ✅ 纯 Go(无 cgo)、StorageBackend/TLSProvider 预留点存在 |

## 2. 本轮修复清单

### 2.1 功能与并发缺陷

| # | 位置 | 问题 | 修复 |
|---|------|------|------|
| F1 | `connector/manager.go` ApplyDevice/RemoveDevice | 读锁外就地修改与存储配置共享底层数组的切片:数据竞争 + 失败回滚后存储配置已被污染 | 先 `cloneConnectorConfig` 再改;RemoveDevice 改为变更成功后才发布离线状态;新增回归测试 `TestFailedReloadDoesNotCorruptStoredConfig` |
| F2 | `configmanager/manager.go` Apply | revision 检查与应用之间释放锁:并发推送时旧 revision 可后到覆盖新配置,FR-S-029a 失效;revisions 还可能回退 | 整个 Apply(幂等检查→revision 比较→应用→记录)在单一互斥锁内串行;新增 50 轮并发回归测试 |
| F3 | `command/service.go` | FR-S-014 重试未实现;超时无按类型默认值 | 按 `CommandOptions.retry_count`(0=不重试)指数退避重试(200ms 起、上限 5s),仅传输/执行错误重试,REJECTED 不重试,记录 DT-CMD-004;CONTROL 默认超时 10s、其余 30s;3 个新单测 |
| F4 | `connector/manager.go` startConnectorLocked | Connector panic 直接崩进程;Start 异常返回后无自动重启(违反 NFR-008/009、DT-CON-003) | runConnector 包 recover;异常退出按 1s→2s→…→60s 指数退避自动重启;实例被热替换/移除后停止守护 |
| F5 | `connector/modbus` ReloadConfig | 仅调 Init:连接参数热更后旧连接继续使用;Init(c.mu)与 poll(opMu)对 cfg/converter 的混合锁数据竞争 | ReloadConfig 持 opMu 串行 + 关闭旧客户端,下次操作按新参数重连 |
| F6 | `connector/modbus` markDeviceState | 持锁起 goroutine 向 upstream 发送:停机时 goroutine 永久泄漏,状态消息乱序 | 锁内构造消息、锁外带 ctx 的阻塞发送;poll 全链路传 ctx |
| F7 | `connector/modbus` BuildTelemetry | 单数据点解码失败丢弃整设备本轮全部数据点(偏离设计 4.4"跳过单条") | 失败点跳过并计入错误数,其余数据点正常上报;全部失败才整条报错 |
| F8 | `connector/mqttdevice` 路由 | 自定义 topic 模板(非 `devices/` 前缀或非标准后缀)时设备 ID 提取与类型路由全部失效,消息静默丢弃 | 模板逐段匹配路由(`matchRoute`),四类 topic 模板可任意自定义;新增单测 |
| F9 | `connector/mqttdevice` ReloadConfig | 热更后不退订旧 topic,残留订阅继续触发回调 | subscribe 时对比新旧 filter 集合,退订失效 topic |
| F10 | `connector/opcua` paramsToArgs | map 遍历顺序随机:多参数 Method Call 实参顺序不可重现 | 按参数 key 字典序排序(约定:key 字典序对应 InputArguments 顺序) |
| F11 | `configmanager` | `DeviceConfigPayload.strategy_overrides`(字段8)被丢弃;`UPDATE_GLOBAL` 是空操作 | strategy_overrides 按 key 写入数据点策略;UPDATE_GLOBAL 经 `GlobalApplier`(Runtime 实现)热更全局策略与背压策略,queue_capacity 拒绝热更并警告 |
| F12 | `internal/errors` | 错误码与接口文档 5.2 冲突(DT-CMD-001~004 语义被挪用;DT-NBI/BUF/STR 码未使用) | 全表对齐 + 扩展码 DT-CMD-006/DT-CON-006/DT-NBI-003;NBI 断连/恢复、BUF 续传开始/完成/高水位/淘汰、STR 背压触发/解除、CFG 成功/失败均按文档码记日志(CSC-005) |
| F13 | `runtime` MetricsResponse | `upstream_msg_per_sec` 等填的是累计总数 | 增量窗口计算真实条/秒;Snapshot 透出策略/背压/订阅者丢弃指标 |
| F14 | `runtime` Publish | 订阅者通道满时静默丢弃(违反"不得静默") | 丢弃计数 `datatransfer_subscriber_dropped_total` 暴露 |
| F15 | 全仓 copylocks ×10 | protobuf 消息按值拷贝(`Devices() []dtv1.DeviceInfo`、`cloneResponse` 值拷贝) | `Devices()` 改 `[]*dtv1.DeviceInfo` 且返回 `proto.Clone` 深拷贝(消除返回内部指针被并发读写的风险);cloneResponse 用 proto.Clone;`go vet` 归零 |

### 2.2 新增基线能力

| 能力 | 实现 |
|------|------|
| 上报策略引擎(FR-S-033~036) | 新包 `internal/strategy`:四种模式、死区、三级覆盖(数据点>Connector>全局,索引由 Connector Manager 维护)、仅作用 TELEMETRY、部分过滤时重建消息不改原消息、策略参数指纹变更自动重建状态(对应设计 4.6 热加载语义)、状态 1h TTL 惰性清理;周期模式为惰性求值(周期内最多放行一条,设备停发不补发旧值);9 个单测 |
| 背压控制(FR-S-037~039) | Manager 消费侧 80% 触发/50% 解除双水位,DT-STR-002/003 日志;BP_BLOCK(默认,通道满时采集自然阻塞)与 BP_DROP_OLDEST(背压期丢队首最旧遥测,STATUS/EVENT/CMD_RESPONSE 永不丢弃);触发次数/丢弃数/队列使用率全部入指标与 GetMetrics |
| 配置面扩展 | YAML 新增全局 `report_strategy`、`backpressure.policy`、数据点级 `report_strategy`;环境变量补 `DT_MQTT_TLS_CERT_FILE/KEY_FILE/CA_FILE`、`DT_BACKPRESSURE_POLICY`;策略模式/周期/死区进入 Validate 校验 |
| 跨服务 JSON 契约钉死 | config 结构体补 snake_case json 标签:`DeviceConfigPayload.datapoints/address`、`ConnectorConfigPayload.connection/polling/converter` 的 JSON 字段名与 YAML 完全一致(CSC-002 硬需求 2) |

### 2.3 性能优化

| # | 位置 | 问题 | 优化 |
|---|------|------|------|
| P1 | `buffer.Enqueue` | 每条消息全表 `SUM(LENGTH(payload))`(5000 msg/s 目标下为 O(N) 热点) | 缓存 usedBytes 原子计数(启动校准、入队增量、清理路径精确重算);仅在缓存超限时进入精确淘汰路径 |
| P2 | `buffer.MarkCompleted/MarkFailed` | 每行一条 UPDATE | 合并为单条 `WHERE id IN (...)` |
| P3 | `buffer.Cleanup` | lease 永久过期的 sending 行不参与 TTL 清理,可能永久残留 | TTL 清理覆盖 pending+sending(TTL 7 天 ≫ lease 30s,无误删风险) |
| P4 | `waitToken` ×2 | 每次发布起一个 goroutine 等 token,ctx 取消后泄漏 | 直接 select `token.Done()` |

### 2.4 安全加固

| # | 位置 | 问题 | 修复 |
|---|------|------|------|
| S1 | `security/tls.go` | `AppendCertsFromPEM` 返回值被忽略:CA 文件无有效证书时静默得到空池 | 校验失败直接拒绝启动并给出明确错误 |
| S2 | `security/tls.go` | 未显式设定 TLS 最低版本 | `MinVersion: TLS1.2` |
| S3 | `security/tls.go` + bootstrap | `insecure_skip_verify` 无任何告警 | TLS 构造与启动时双重 WARN 日志 |

## 3. 已知边界(诚实声明)

1. **Modbus 单连接串行**:poll 全程持 opMu,长轮询周期会阻塞同 Connector 的下行指令;对 200ms 下行目标的影响待压测量化(DT-COL-014)。
2. **BP_DEGRADE 回退 BP_BLOCK**:内置 Connector 尚无运行时降频接口(DT-COL-015)。
3. **OPC-UA native Subscription 为空实现**,订阅路径仅 fake client 验证;Reload 不重建 OPC-UA 会话(DT-COL-016)。
4. **周期上报策略为惰性语义**:周期内最多放行一条、设备停发时不重发旧值;若上游需要"无数据也按周期重发最新值"语义需另行确认。
5. **方案A 内存队列**:重启丢失(设计 DQ-007 已锚定,运维文档须标注)。
6. **南向凭据明文存放于 YAML/推送配置**:凭据加密/托管属平台级方案,未在本服务范围内。
7. mqtt_device 的 `connection.host` 被用作 client_id 的历史行为保留(为兼容现有配置),默认 `dt-{connector_id}`。

## 4. 验证记录

| 验证项 | 结果 |
|--------|------|
| `go build ./...` | ✅ |
| `go vet ./...` | ✅ 0 告警(此前 10 处 copylocks) |
| `go test -race -count=1 ./...` | ✅ 16 包全部通过 |
| 新增测试 | strategy 引擎 9 项、command 重试 3 项、configmanager 并发回归 1 项(50 轮)、manager 回滚回归 1 项、mqttdevice 路由 1 项、示例配置加载 1 项 |
| 启动 smoke | ✅ healthz/readyz 200;/metrics 含 backpressure/strategy/per-connector 指标;BP 策略从配置生效(DT-STR-001 日志) |
