消息基础设施向第三阶段控制专家交接

当前生产协调实现保留 `Platform/internal/coordination/coordinator.go` 的既有格式与流程，本轮改动范围为持久迁移验证夹具及现有集成测试的定义准备。`integration_test.go` 现在为三台节点分别保存符合当前契约的定义，调用 `Definitions.PrepareDefinition` 生成发布计划；竞争、失效执行权、检查点恢复与法定多数失效的原断言继续执行，实际目标服务为 NATS 2.14.7。

签名封装为 `node_id`、`payload`、`signature`，签名采用节点 Ed25519 密钥，输入前缀为 `sf-coordination-v1:<node_id>:`。验证其他节点时从本地 entity 对象读取 active edge 的 `audit_public_key`。迁移夹具使用固定签名密钥保存三个 SQLite 节点数据库，R3 JetStream journal 内容经三个节点各自 Recover 验证，旧检查点 SHA-256 为 `60855a` 前缀，完整摘要以原始 checkpoint JSON 和总报告为准。

每个前缀拥有 state、lease、journal、active 四个文件型 KV，副本数为 3、History 为 1、每桶 MaxBytes 为 512MiB、MaxValueSize 为 2MiB。state 使用 30 秒 TTL，lease、journal、active 的桶 TTL 为 0；lease 内容中的 expires_ms 负责执行权到期。执行权序号来自 JetStream CAS 修订，序号会跨越多次更新，当前夹具取得 1 后接管得到 5。第三阶段应按大小关系验证序号，保留已保存值而非要求连续加一。

已完成的真实迁移流程为 2.11.9 写入旧检查点，再经 2.12.15 逐节点进入 2.14.7；原服务器身份和各自文件目录保持一致。旧检查点修订 1 在每次逐节点替换后均保留；edge-b 接管后修订成为 2，edge-a 的旧 Validate 与 Checkpoint 都失败。单节点与全节点重启保留新序号、步骤、身份和签名内容，法定多数失效用例恢复后原迁移检查点仍可读取。

本轮为第三阶段提供格式兼容与运行证据，业务状态机和回放比较仍按第三阶段目标实施。后续新增 Execution 状态、步骤身份、补偿信息或检查点版本时，协调专家需同步兼容读取、签名格式与跨版本恢复用例；原有 message_upgrade 前缀数据可以作为旧格式夹具。active、lease 与 journal 的约束及控制动作幂等仍以当前生产实现为准。

完整原始证据位于 `/private/tmp/sf-message-nats-20261005-final/verification.json`，实际命名用例为同目录 `command-080.txt`，法定多数恢复后的持久验证为 `command-081.txt`。测试二进制 SHA-256 为 `77558d5d410093b901a1cef6fefe5ca323a6778d79844eca75abca0c630e33de`，使用 Go 1.27.1 构建；源码、模块和完整摘要范围在 `.local/evolution/message-infrastructure-verified.json` 登记。临时 NATS 服务及网络均已删除，三个节点数据和冷备仅作为证据保存在临时文件系统。
