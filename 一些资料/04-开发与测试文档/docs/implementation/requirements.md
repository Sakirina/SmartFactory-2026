# 需求与验收追踪

本表记录 2026-09-20 的实现与实际证据。各项完整验收仍须在 UOS 目标系统执行；表中“本机通过”对应 macOS 与 Linux 容器测试环境，持续时长按报告实测值填写。

| 编号 | 需求与实现 | 已有验证 | 仍需执行 |
|---|---|---|---|
| A01 | 固定 CE/Edge 版本；启动停机与恢复；`deploy`、`scripts`、`bootstrap` | 进程生命周期回归、[干净部署原生回调](../evidence/native-clean-deployment.json)、[四种配置检查](../evidence/deployment-profile-matrix.json) | UOS 安装、主机重启、代理托管和自托管两套部署；原生服务持续运行与内存预算 |
| A02 | 三类协议和进程插件；`DataTransfer/internal/connector` | MQTT 3.1.1/5.0；Modbus；OPC-UA 加密证书、方法、订阅恢复；插件崩溃隔离 | UOS 环境复验 |
| A03 | 字节顺序、单位、位字段、反向编码和质量 | 三类协议各 1000 组转换、精确整数及浮点容差测试通过 | UOS 环境复验 |
| A04 | 200 Modbus、200 MQTT、100 OPC-UA，60 分钟 5000 消息/秒 | [实测](../evidence/capacity-disk-60m.json)：18654020 条，5181.65 条/秒，输入与记录一致 | 优化后的当前构建在 UOS 复验；批量消息另行测量 |
| A05 | RSS ≤256MiB，至少 1000 次控制 p99≤200ms | 同次测试最大 RSS 58.50MiB，控制 p99 18.584ms | 当前构建 UOS 复验 |
| A06 | 提交确认、冲突、去重、丢确认与进程恢复 | [10 万事件](../evidence/A06-durable-100000.json)：3 次进程重启、14285 次丢确认，已确认缺失 0，重复业务效果 0，写失败成功确认 0 | UOS 复验 |
| A07 | 会签、组织关系、时限、条件变化、联锁和结果待核对 | `control_test.go`、命令持久化测试；实际会签后 Modbus 状态改变 | 目标部署中复验全拒绝矩阵 |
| A08 | 500 设备，断云 24h，100Mbps/20ms 链路，6h 补传补算 | mTLS 同步、游标恢复、离线权限、计数断连和历史补算测试 | 完整 24h 断云及六小时恢复测量 |
| A09 | 三节点各 50 设备，断云 24h，100 次故障 | [150 设备、100 次故障](../evidence/site-150-faults-100.json)：重复物理动作 0，运行 1333.94 秒 | UOS 连续 24h 与目标系统故障复验 |
| A10 | 配置排队、版本保护、重启恢复与失败原因 | 配置中心实际动态更新、失败重试、同版本内容冲突、采集实体恢复 | UOS 重启场景复验 |
| A11 | 全聚合、质量过滤、历史资产/规则版本和修订 | `engine`、`store` 测试；72h 历史计数恢复；压缩归档参与补算 | 目标环境确定性数据集复验 |
| A12 | 各层保留、永久对象、容量和压缩归档 | 可控时间、PostgreSQL 分区优先删除、完整归档校验与局部到期；[容量报告](storage-and-io.md) | 实际 24h 存储测量及 TB 投影、备份总量 |
| A13 | 告警激活、恢复、确认、历史修订和通知抑制 | 原始及聚合输入、历史告警保留确认、通知渠道配置和重投抑制测试 | 客户提供邮件/短信服务后的外部送达检查 |
| A14 | 组织变更、离职、授权、离线到期、TOTP 与审计 | 权限与组织回归、凭据封装、签名审计；修改/删除/插入/乱序均报告异常 | UOS 多节点汇总复验 |
| A15 | 20 订阅、10 查询、20 序列、2000 点，p95≤2s | [180 秒 API 测量](../evidence/api-postgres-180s.json)：历史 1.564s、SSE 1.096s；[20 浏览器会话](../evidence/browser-latency-20.json)显示 p95 798ms | UOS 正常负载持续复验 |
| A16 | 独立云端、现场、大屏及权限与失败状态 | [八项浏览器检查](../evidence/frontend-results.json)；1440×900、390×844、1920×1080 | 24h 断网过程中的页面复验 |
| A17 | 页面助手和 MCP 三类草稿、工具调用及权限拒绝 | [页面检查](../evidence/frontend-results.json)、[外部 MCP](../evidence/mcp-client.json)、服务端越权测试 | UOS 部署复验 |
| A18 | 备份实体、规则、权限、密钥、审计和未完成任务 | [SQLite 应用恢复](../evidence/backup-recovery.json)、[PostgreSQL 数据恢复](../evidence/postgres-backup-recovery.json)、归档恢复测试 | UOS 主机与完整原生组件恢复 |

原基线的 158 项编号分别对应到 [实现与验证入口](baseline-trace.md)，服务组织、数据契约、存储与国产化范围的调整依据见 [方案调整记录](decisions.md)。

## 原需求归属

| 原需求组 | 实现位置 | 相关验收 |
|---|---|---|
| FR-GW-001 至 031 | `DataTransfer`、`Platform/internal/datatransfer`、`thingsboard`、`cloudsync` | A02 至 A10 |
| FR-DP-001 至 030 | `Platform/internal/store`、`engine`、`Frontends/src/views.tsx` | A03、A08、A11 至 A13、A15 |
| FR-OC-001 至 034 | `Platform/internal/engine`、`control`、`coordination` | A07、A09、A11、A13 |
| FR-AM-001 至 005 | `Platform/internal/api/business.go`、`cloudsync/assets.go` | A10、A11、A14 |
| FR-IAM-001 至 005 | `Platform/internal/identity`、`plugins` | A07、A14 |
| FR-AU-001 至 005 | `Platform/internal/store/audit.go`、`cloudsync` | A14、A18 |
| FR-CFG-001 至 007 | `Platform/internal/configcenter`、`cmd/sf-config` | A10、A14、A18 |
| AI/MCP 与独立页面 | `Platform/internal/ai`、`Frontends`、`examples/mcp` | A15 至 A17 |

## 五个场景

| 场景 | 可编辑定义和实际验证 |
|---|---|
| 温湿度 | 高低温、湿度上下限、回差与设备状态反馈，见 `examples/factory/definitions.json` 和 [场景证据](../evidence/five-scenes.json) |
| 红外照明 | 有人点亮、无人延时关闭、短暂感应与第二次到访回归 |
| 危气 | 烟雾、可燃气、CO 分别触发告警和排风，跨节点预案在独立现场配置中验证 |
| AGV | 障碍停车、恢复继续；过期数据、质量和物理联锁由策略及控制测试验证 |
| 计数 | [10000 个新增有效事件](../evidence/counter-reliability.json)经历重复、重启、断连后，云边累计值一致 |

达梦数据库和国密专项适配按本轮决定列入后续扩展；本次目标为 UOS Server V20 x86_64、PostgreSQL 和标准 mTLS。完整原始数据保留与压缩归档采用 2026-09-20 追加确认的方向。
