# MQTT v5 实验验证记录

日期：2026-06-09

## 目标

本实验用于判断 DataTransfer 的方案B北向 MQTT 链路应采取“兼容、升级或保持 v3.1.1”的哪一种路线，并明确后续迭代时机。实验不修改生产 MQTT Adapter，只通过自动化测试验证 MQTT v5 能力与现有 v3 payload 合约能否共存。

## 验证范围

- MQTT v5 客户端：`github.com/eclipse/paho.golang v0.23.0`
- 当前 MQTT v3 客户端：`github.com/eclipse/paho.mqtt.golang v1.5.1`
- 内嵌 Broker：`github.com/mochi-mqtt/server/v2`
- 测试入口：`internal/northbound/mqtt/v5_spike_test.go`

## 结果

1. v5 客户端可以连接现有内嵌 Broker，并通过当前 `dt/v1/up/{gatewayId}/telemetry` topic 发布 `DeviceMessageBatch` Protobuf payload。
2. v5 发布端携带的 `User Properties`、`Response Topic`、`Correlation Data` 可以被 v5 订阅端收到，适合承载 `message_id`、`command_id`、`gateway_id` 等追踪字段。
3. v3 订阅端可以收到同一条 v5 发布的 Protobuf payload，说明现有 topic 与 payload 合约可以和 v5 发布端共存。
4. v5 properties 对 v3 订阅端不可见；混用 v3/v5 时，跨服务必须继续依赖 payload 内字段完成业务语义，不能把关键语义只放在 v5 properties。
5. `Message Expiry` 不适合作为 DataTransfer 本地缓冲 TTL 的唯一依据。本次请求 604800 秒，Broker 转发为 86400 秒；SQLite Buffer 的 TTL/容量清理仍应作为方案B可靠传输的权威策略。
6. `autopaho` 提供自身队列能力，但 P2 已经实现 SQLite Buffer、lease 和 Replay Worker。后续若引入 v5，默认应使用阻塞发布确认路径，不启用第二套客户端队列，避免重复排队和确认状态不一致。

## 结论

采取兼容路线：P3 默认生产链路继续保持 MQTT v3.1.1 与现有 P2 可靠传输实现；MQTT v5 不作为 P3 主线的强制升级项。v5 的价值集中在跨服务追踪与标准化请求响应元数据，适合作为可选 adapter 能力，在 Broker 与上游消费者均确认支持 v5 后再进入生产实现。

## 后续时机

- P3 开工时：保持默认 v3.1.1，避免影响配置热加载、Discovery 对接、扩展 Connector 等主线任务。
- P3 内若修改 MQTT Adapter 边界：保留 `protocol_version` 配置入口或内部 client interface，但不要求默认启用 v5。
- P3 后或集成验收前：在实际 Broker 与上游消费者均通过 v5 User Properties 透传验证后，再实现可选 MQTT v5 adapter，并继续保留 v3.1.1 兼容路径。
