# 第二阶段后台任务接口

任务接口使用 `/api/sf/v1` 前缀、现有 Bearer 身份和精确 JSON 数值解析。OpenAPI 由 `model.Task`、`model.TaskAction`、`application.TaskList` 和 Huma 注册共同生成；前端执行 `npm --prefix Frontends run generate:api`，通过正式 openapi-typescript 从 `contracts/1.0/openapi.json` 生成 `contracts/1.0/api-types.ts`，Go 导出器负责 JSON Schema 与 OpenAPI。实现位于 `Platform/internal/api/task_routes.go`、`internal/application/tasks.go` 和 `internal/store/tasks.go`，队列执行由官方 River v0.48.0 承担。本文描述当前实现的接口约定，实际 worker、恢复及并发验收结果另存于存储验证报告。

`GET /tasks?after=<上一页next>&limit=100` 返回 `{ "items": [...], "next": "..." }`；`limit` 范围为 1 至 500，默认 100。服务端按照任务 ID 翻页，并逐项过滤当前身份无权读取的资源，因此一页可能少于 limit，也可能为空而 next 仍非空。`GET /tasks/{id}` 返回单项详情。路径中的任务 ID 必须完整转义，冒号属于身份内容。

每项详情同时保留四种关联：`id` 是本平台任务身份，`business_id` 与 `business_version` 指向创建任务时的业务记录，`outbox_id` 指向原投递记录，`river_id` 是正式 River 行号。`resources` 列出任务所涉资源；补算包含明确设备以及指定定义对应的分组、选择设备、资产和控制条件／步骤设备；只给出设备时按设备授权，定义无法读取时要求全局资源 `*`。外部定义投影使用相同定义资源关系，归档采用全局资源 `*`。

任务详情示例如下，示例的版本摘要仅用于展示字段结构：

```json
{
  "id": "recompute:manual:example:1",
  "kind": "recompute",
  "business_id": "manual:example",
  "business_version": 1,
  "river_id": 42,
  "queue": "recompute",
  "resources": ["sensor-1"],
  "state": "retryable",
  "business_state": "failed",
  "phase": "replaying",
  "replay_id": "recompute:manual:example:1",
  "attempt": 2,
  "max_attempts": 8,
  "next_ms": 1791090000000,
  "created_ms": 1791089990000,
  "started_ms": 1791089999000,
  "error": "history source temporarily unavailable",
  "progress": 0.4,
  "cursor_ms": 1791089000000,
  "version": "opaque-snapshot-token",
  "superseded": false,
  "allowed_actions": ["cancel", "retry"]
}
```

`progress` 使用 0 至 1 的比例，页面显示百分比时乘以 100；补算进度和 `cursor_ms` 来自完整历史窗口提交后的业务检查点，同一时间戳的全部输入完成后才推进游标。`phase` 为 `replaying`、`rollups` 或 `completed`，新建任务尚未建立恢复记录时省略；`replay_id` 在首次执行建立后保持不变。逐点计算完成时 `progress=1`，接着进入 `phase=rollups`，正在执行的业务状态显示 `business_state=finalizing`；页面此时显示“历史计算已完成，正在更新汇总”。任务完成依据 River `state=completed`，补算同时使用 `phase=completed`，不要仅凭 progress 隐藏运行状态。失败和取消保留当前 phase，business_state 分别为 failed 与 cancelled。`attempt` 是 River 已开始的执行次数，首次入队为 0。`next_ms`、创建、开始、结束和取消请求时间均为 Unix 毫秒。`error` 展示最近失败原因。`superseded=true` 表示同一业务已有更新的补算任务，旧任务不会重新执行更新后的业务。

`state` 沿用 River 的 `available`、`scheduled`、`pending`、`running`、`retryable`、`completed`、`cancelled` 与 `discarded`。`retryable` 等待自动重试，`discarded` 表示本轮尝试次数已用完。`cancel_requested_ms` 非零且 `state=running` 时，页面显示“取消请求已提交，正在停止后续处理”。可执行按钮完全采用服务端的 `allowed_actions`，读取角色、资源许可和任务状态都会影响该数组；空数组时只展示详情。

`POST /tasks/{id}/cancel` 与 `POST /tasks/{id}/retry` 提交以下请求；`expected_version` 为详情中的完整 `version` 字符串，前端把它视为不透明值。

```json
{"expected_version":"opaque-snapshot-token"}
```

两个操作均再次核对角色、全部资源、授权记录与集合版本、任务版本及 River 实际状态，成功返回更新后的详情。版本或状态变化返回 409，前端刷新详情后呈现新的可用操作。权限不足返回 403，身份失效返回 401，任务不存在返回 404。`POST /jobs` 创建补算入口继续返回 HTTP 202 与 Job，新建或更新的 pending 补算会在同一事务内加入 River。旧的 `POST /jobs/{id}/retry` 也返回 HTTP 202 与 Job，并通过同一 Tasks 应用用例、角色／全部资源／授权版本及 River 状态检查恢复原有任务；旧入口读取当前任务版本，和新入口并发时一项成功、另一项收到 409。原 River 历史已过期时返回 404，被新任务代际取代或当前状态不允许重试时返回 409。

`version` 标识需要人工确认的任务操作。相同任务与运行代际内，River 的获取、等待后重调度、尝试次数和下次执行时间，以及业务游标、进度和逐步累积结果的更新，均保留当前操作版本；页面可以在正常阅读后提交先前取得的完整版本。任务身份或资源、持久输入快照、业务阶段、完成／失败／取消状态及人工操作代次发生变化时，版本随之变化。历史任务等待父代时，父代完成但子任务尚未解析初始状态，仍沿用当前等待操作；子任务保存解析后的快照并开始执行时更新操作版本。取消事务读取并保存当时最新的业务游标与已提交结果。

服务端在 River metadata 中保存内部 `sf_operation_revision`，既有任务缺少该字段时使用 0；每次成功取消或重试均在任务事务内递增。取消通过官方驱动合并 metadata；重试在业务对象和 River 行受到事务锁保护时读取最新 metadata，删除旧 `cancel_attempted_at`，保存新的操作代次，再使用官方驱动的 `JobUpdateFull` 写回其余字段的原值。River 的其他字段、`sf_trace`、自定义值及 JSON null 均继续保留。该代次与业务变更、River 状态及审计共同提交，审计失败时共同回滚；人工重试继续使用原任务与快照，但此前取得的操作版本失效。角色、资源归属和授权记录仍在每次操作及其提交事务中核对。

运行中的取消请求需要等待原 worker 完成本次返回，再进入可重试状态；此期间提交重试返回 409。明确重试会撤销此前的取消意图，等待父代的任务可以继续多次生产调度，页面持续显示等待父代并允许再次取消。SQLite 官方 merge 将 null 解释为删除字段，PostgreSQL 官方 merge 则保留值为 null 的字段，而其取消判断读取字段是否存在，因此重试统一删除字段后写回。重试后的新取消产生新的取消意图，原操作版本和重复重试继续返回 409。

取消补算会提交取消状态并增加业务版本，正在执行的补算收到上下文取消或发现版本变化后停止；此前已提交的历史派生结果、修订和检查点继续保存，已经提交的正式累计值与告警修正也继续保存。取消不回滚此前已经完成的数据库事务。重试沿用任务、River 行号、业务身份和 replay_id。首次执行保存选定定义、设备范围、固定的历史起止时间以及起点之前的状态快照；后续尝试从最近完成的完整窗口之后继续，未完成窗口按保存的派生输出回执恢复，父计算已提交时仍能继续下游计算。完成逐点计算与正式状态提交后，phase=rollups 的尝试只继续分钟、小时与日汇总；每个汇总写入事务再次核对业务版本，取消或新任务代际会停止旧尝试的后续写入。晚到数据触发新的 pending 业务版本时建立新 task_id，原任务显示 superseded。页面取消确认文案应为“停止后续补算，保留已提交的历史结果和修订”；成功文案根据实际状态区分“已取消”和“已提交取消请求”。

外部投影取消后，原 outbox 身份继续保留，River 停止后续尝试；已发送到外部服务的请求可能已经处理。再次尝试沿用相同 outbox 身份，由现有投影逻辑核对业务结果。设备控制仍使用控制执行记录及其状态流程。

`GET /task-queues` 返回各处理类别的并发、批次、超时、调度间隔，以及等待、运行和耗尽重试的数量，要求读取全局资源 `*`。当前预算按应用进程计算：采集最多 4 个并发请求、每批最多 10000 点、30 秒；控制入口共享整个 `Control.Run` 生命周期最多 3 个并发的预算，等待预算期间可通过上下文取消；后台投递每批 20 条、100 毫秒间隔；实时分析与策略各 1 条消费流程，每批 256 条，间隔分别为 50 和 20 毫秒；补算并发 1、每次执行 30 秒；归档并发 1、每块最多 4096 点、8 MiB 原文、每次最多 16 块及 5 秒；外部投影并发 2、单次 15 秒。数据库连接池 PostgreSQL 上限为 12，SQLite 维持单连接，编码、压缩和外部请求在写入事务之外执行。

任务执行历史由 River 保留 30 天；详情列表查询仍存在的 River 行，过期详情返回 404。业务任务映射保留用于原身份去重。`tasks.wait`、`tasks.execute`、`archive.prepare` 与 `archive.commit` 使用固定 span 名称，task_id、business_id、queue 和 attempt 作为追踪属性；等待直方图只使用有限队列名称，业务身份不会作为指标标签。

迁移前尚无任务映射的 failed Job 在首次重试时完成一次承接：授权与定义版本校验、原 Job 版本更新、sf_tasks 映射、正式 River 入队及审计共同提交，后续重试使用该映射。已有恢复元数据会继续保留；更早版本的 Job 若尚无固定范围和起点快照，首次执行会从持久原始历史建立恢复记录，再按完整窗口推进。创建新补算仍生成新的任务代际。
