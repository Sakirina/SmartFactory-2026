第三阶段控制执行与人工操作接口

本文描述已经完成后端验证的控制接口，生成契约以 `contracts/1.0/openapi.json` 为准；验证命令、源码与二进制摘要保存在 `.local/evolution/control-execution-verified.json`。执行状态与人工操作状态分别显示；云端提交返回 `202` 时，设备执行状态保持最近一份边缘回执中的值，操作记录显示“等待边缘处理”。

`GET /api/sf/v1/executions/{id}` 返回 `ExecutionDetail`，包含 `execution`、`allowed_actions`、`timeline`、`evidence`、`operations`、`source_node_id` 和 `source_version`。`execution.version` 是当前应用的本地文档版本，`source_version` 是该应用掌握的目标边缘文档版本；现场应用返回自己的执行版本。云端尚未收到边缘记录时，`source_version` 为 `0`。

| 执行状态 | 页面含义 |
|---|---|
| awaiting_approval | 等待会签 |
| approved | 会签已满足当前要求 |
| queued | 等待开始执行 |
| running | 正在执行正常步骤 |
| degraded_running | 正在执行已配置的降级步骤 |
| result_unknown | 原命令结果待核对，自动执行已停止 |
| ready_to_resume | 已核对原命令结果，等待人工恢复剩余步骤 |
| completed | 正常步骤已完成 |
| degraded_completed | 降级步骤已完成 |
| failed | 已取得明确失败结果 |
| rejected | 当前条件、授权或版本未满足执行要求 |
| cancelled | 取消已处理，后续步骤停止 |
| cancelled_result_unknown | 后续步骤已停止，已预留或已发送命令的结果继续核对 |

`allowed_actions` 包含 `cancel`、`reconcile`、`resume` 三项，每项提供 `allowed` 与拒绝原因 `reason`。页面根据这些字段提供按钮；点击后仍会在应用用例中检查最新身份、资源权限、对象版本、状态和执行权。运行中的取消请求会停止尚未执行的步骤，已经发送的动作保留原 `command_id`，后续反馈继续归属该命令。

人工操作采用以下流程：

1. 读取执行详情，保存 `execution.version`、`source_version` 和用户填写的操作理由，为此次请求生成一个 `operation_id`。
2. 向 `POST /api/sf/v1/executions/{id}/cancel`、`/reconcile` 或 `/resume` 提交 `ExecutionAction`。请求包括 `expected_version`、`expected_source_version`、`operation_id`、`reason`，可附带毫秒时间戳 `deadline_ms`。云端必须填写 `expected_source_version`；省略截止时间时，使用当前审批时限作为操作有效期，显式截止时间最多允许到当前时刻之后 24 小时。
3. 保存返回的 `ControlOperation.id`。云端返回 `202` 与 `status=pending`，现场处理完成后返回 `200`；使用 `GET /api/sf/v1/control-operations/{id}` 和执行详情刷新结果。同一操作编号与相同请求内容可以重复提交，内容变化返回 `409`。
4. 操作状态成为 `completed`、`rejected` 或 `expired` 后，分别显示执行结果、拒绝原因或边缘处理前已过期。`completed` 表示这次人工操作已处理；核对后若执行仍为 `result_unknown`，页面继续显示待核对。恢复完成受理后，执行先变为 `queued`，随后由设备执行流程更新。

请求示例：

```json
{
  "expected_version": 12,
  "expected_source_version": 8,
  "operation_id": "operator-check-20261005-01",
  "reason": "核对原命令的网关持久回执"
}
```

`ControlOperation` 提供 `id`、`execution_id`、`action`、`status`、`origin_node_id`、`target_node_id`、两个期望版本、请求与截止时间、开始处理时间 `started_ms`、处理完成时间 `processed_ms`、操作者、理由、错误、`result_version`、`result`、本地 `version` 和可用时的 `trace_id`。边缘在首次开始处理时事务性保存 `started_ms`，并发送独立的开始处理回执；重复投递及进程重启沿用这个时间。有效期按首次开始时刻判断，期限内开始且期限后完成的操作仍可完成；云端按 `started_ms` 核对。`pending` 且 `started_ms > 0` 时，页面可显示“已开始处理，等待结果”；`started_ms` 缺失时显示“等待边缘处理”。时间线分别提供 `operation_request`、`operation_started` 和 `operation_result`。`result_version` 对应边缘执行记录，页面使用 `execution.version` 提交当前应用的版本条件。

`timeline` 将转换、审批、动作、反馈以及人工操作请求和结果放在同一时间轴。转换项包含事件、来源状态、目标状态、本地版本、来源版本、操作者、理由、命令身份及追踪身份；反馈同时保留来源的 `observed_ms` 与平台的 `collected_ms`。老记录从原审批、动作日志或审计中读取实际时间，`source` 标明出处；缺少的时间保留为缺失值或 `0`。

核对通过只读命令查询完成，页面无需也不能上传一个成功结果来替代设备反馈。`CommandEvidence` 提供命令、设备、步骤、反馈来源、时间、请求摘要、内容摘要、状态、是否可信及拒绝原因。命令身份、设备、请求内容、来源和时间符合要求，且结果为明确的 `SUCCESS`、`FAILURE` 或 `REJECTED` 时，核对流程才更新原动作结果。查询不到记录、反馈过期、旧记录缺少绑定信息或结果仍未知时，页面展示相应证据，执行继续等待核对。

边缘的执行回执同步该执行已经保存的全部命令证据，云端详情的 `evidence` 和 `feedback` 时间线保留原证据编号、命令身份、来源、时间与追踪身份。取消后迟到的原命令响应以 `control.action_journal:<节点>` 标明平台动作日志来源；只读核对继续保留设备命令日志的来源。回执的可选 `receipt_source` 记录来源节点、执行版本及记录时间，云端将它与证据写入同一事务和签名审计。较旧回执可以补充证据，当前执行状态与来源游标仍保留较新版本；相同证据编号出现不同内容时，整份回执返回冲突。原格式回执继续有效，公开详情字段沿用现有生成契约。

原创建、审批、下发地址保持一致。审批请求可以携带 `expected_version`；下发可以使用包含该字段的请求体，原有空请求体调用继续有效。身份失效返回 `401`，角色或资源不满足返回 `403`，对象缺失返回 `404`，版本与内容冲突返回 `409`，业务状态或请求字段不满足要求返回相应错误正文中的说明。

前端验收应包含云端待处理、现场完成、不同资源权限、版本冲突、取消后的待核对状态、没有可信反馈时继续等待，以及核对成功后只恢复剩余步骤。浏览器中的状态应与执行记录、操作记录、动作次数和时间线逐项核对。

浏览器验证夹具使用实际云端和边缘 `Application.Run`、独立 DataTransfer 进程、持久命令日志及 Modbus 计数模拟器。准备和验证流程如下：

1. 使用本项目配置的 Go 环境运行 `python3 scripts/control-intervention-fixture.py start --output /tmp/sf-control-browser`，输出目录必须为空。脚本编译两份测试二进制和三份生产程序，保存 `manifest.json` 与 `build-inputs.json`，记录实际编译依赖、嵌入文件、模块锁定文件、工具链和二进制摘要；随后启动服务并给出云端、边缘地址及私有访问文件位置。运行期用户和密码保存在权限受限的 `runtime/private-access.json` 中，前端静态资源来自 `Frontends/dist`，也可将开发服务器代理到给出的应用地址。
2. 打开 `recover-browser` 执行详情，它已经进入 `result_unknown`。从边缘页面核对后应成为 `ready_to_resume`，人工恢复后只有 `remaining` 新增一次实际动作；原 `first` 命令保持一次。
3. `cancel-browser` 已完成会签，正式下发后第一步会等待模拟回执。此时取消应显示 `cancelled_result_unknown`；向私有访问文件中的 `driver_url` 发送 `POST /release-cancel` 后，第一步成功反馈得到保存，执行成为 `cancelled`，`remaining` 保持零次。
4. 如需从云端观察 `202`，在处理 `recover-browser` 前向夹具控制地址发送 `POST /offline`，再从云端提交核对；操作显示等待边缘处理，执行维持结果待核对。发送 `POST /online` 后继续观察操作回执。
5. 运行 `python3 scripts/control-intervention-fixture.py status --output /tmp/sf-control-browser`，比较执行、时间线与 `runtime/physical-actions.json` 中按命令身份记录的次数。结束后运行同脚本的 `stop` 子命令，两个应用及 DataTransfer 进程共同退出；输出目录保留证据。

模拟器只在测试夹具中使用。故障通过协议调用完成后的回执丢失或延迟产生，控制请求继续经过实际应用用例、gRPC 适配器、DataTransfer 命令日志与 Modbus Connector。
