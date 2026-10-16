# 第三阶段历史分析与影子计算接口

接口采用 `/api/sf/v1` 前缀、现有 Bearer 身份和精确 JSON 数值编码。类型由 `historymodel` 中的 `HistoryRequest`、`HistoryRun`、`HistorySnapshot`、`HistoryStep` 和 `ShadowCandidate` 等结构生成，页面使用正式 `contracts/1.0/openapi.json` 及生成客户端。以下约定对应第三阶段实现，实际验收结果由主协调者维护。

## 历史回放与版本比较

`POST /analysis-runs` 创建独立分析运行，返回 HTTP 202 和 `HistoryRun`。`kind=replay` 按每条观测的 `observed_ms` 选择当时生效的定义版本，使用该版本的持久编译计划；资产归属采用同一观测时刻已经生效的资产版本。定义已经停用或观测超出该版本选择范围时，结果分别记录 `definition_inactive` 或 `outside_selector`。每个历史定义版本拥有自己的节点状态。

`kind=compare` 使用 `left_version` 和 `right_version` 明确指定两个已发布版本，两侧共用一份输入、历史、初始状态、排序与时钟。相同版本也可以比较，用于核对一致结果。每项结果保存左右计划身份、节点解释和独立状态，并按数值、质量、告警、状态、节点路径和触发判断返回差异。

```json
{
  "kind": "compare",
  "definition_id": "production-rule",
  "left_version": 1,
  "right_version": 2,
  "from_ms": 1800000000000,
  "to_ms": 1800000060000,
  "device_ids": ["counter-1"],
  "keys": ["count"],
  "order": "event_time",
  "initial_state": {},
  "clock": {"freshness_ms": 5000}
}
```

所有时间值使用 Unix 毫秒，`step_ms`、`freshness_ms` 和选择器窗口采用毫秒间隔。请求省略 `points` 时，服务端在明确的时间和设备范围读取保留数据；使用资产选择器且规则未列出设备时，必须提供 `device_ids`。显式 `points` 按请求内容创建快照，每项要求已有观测 ID、设备、字段、正数时间戳和 `GOOD`、`BAD` 或 `UNCERTAIN` 质量。来源、原始数值、单位、设备时间、来源序号、规则及资产版本、迟到标记和修订号都随观测保存。

省略 `history` 时，服务端读取所需窗口和输入字段的实际历史；显式 `history: []` 使用空历史。省略 `asset_versions` 时读取涉及设备及祖先资产的版本。`initial_state` 按计算实体和节点 ID 两层组织，省略或传入空对象使用空初始状态，快照的 `initial_state_source` 明确记录采用方式。历史分析创建后始终使用保存的内容，后台重试沿用同一快照及已提交游标。

`order=event_time` 按 `observed_ms`、`source_sequence`、`id` 升序执行，`provided` 使用提供的数组顺序。默认逻辑时刻和新鲜度参照时刻采用观测时间；`clock.at_ms` 固定首个逻辑时刻，`clock.freshness_at_ms` 可以单独固定首个新鲜度参照时刻，后续各项按 `step_ms` 增加。每项结果给出实际采用的时钟。

## 运行、快照与节点解释

`GET /analysis-runs?after=<上一页next>&limit=100` 按运行 ID 分页，返回 `items` 和字符串 `next`；`limit` 为 1 至 500，空 `next` 表示当前查询结束。服务端逐项过滤访问权限，因此页面应按游标继续读取，允许一页为空而游标仍然存在。

`GET /analysis-runs/{id}` 返回运行详情。`task_id` 关联现有后台任务详情及取消、重试入口；`status` 为 `pending`、`waiting_parent`、`running`、`failed`、`cancelled` 或 `completed`。`cursor` 表示已经原子提交的输入数量，`progress` 为 0 至 1；页面百分比需要乘以 100。任务实际完成仍以 `/tasks/{task_id}` 的 River 状态为准。

`GET /analysis-runs/{id}/snapshot` 返回实际采用的完整快照，其中 `snapshot_id`、`sha256`、`input_sha256` 和 `history_sha256` 分别关联保存身份、完整内容、共同计算条件与历史内容。两版本比较使用同一个 `input_sha256`；两侧 `plan_id` 和 `plan_sha256` 在结果中分别展示。快照包含定义及其生效时间、资产版本、实际排序后的输入、历史、初始状态和时钟，调用者每次查询均需拥有全部相关资源的读取权限。

`GET /analysis-runs/{id}/steps?after=0&limit=100` 返回 `items` 和整数 `next`。这里的 `after` 是从零开始的输入下标，包含该下标；`next=-1` 表示本次已读到当前可用结果末尾。运行尚未完成时，页面可以继续使用最后下标加一轮询。每项 `lanes` 包含求值结果 `evaluation`，其 `trace` 按节点提供输入、输出、之前和之后的状态、跳过标记与错误；`input_ids` 和 `asset_versions` 可用于展示来源。`action_intents` 保存候选规则产生的步骤意图。

回放、比较和影子计算通过 `history_analysis` 队列执行，每个应用进程并发为 1，每次尝试限时 30 秒。每份运行最多 10000 个输入、40000 条历史和 40000 个资产版本，快照最多 32 MiB。创建时超出预算、范围内无保留输入或计划尚未准备会返回包含原因的请求错误；执行失败保留错误、快照及游标，可以通过任务入口重试。

## 影子候选与实时观测

`POST /shadow-candidates` 启用候选。请求包含 `id`、`definition_id`、已发布的 `version`，以及可选的 `device_ids`、`keys`、`initial_state`。资产范围候选需要明确设备列表。新候选使用 `expected_version=0`；替换或重新启用已有候选时，提交当前详情的 `version`。版本冲突返回 409，成功响应中的 `epoch` 增加一次，原有运行和输入回执继续保留。

```json
{
  "id": "counter-candidate",
  "definition_id": "production-rule",
  "version": 2,
  "expected_version": 0,
  "device_ids": ["counter-1"],
  "keys": ["count"],
  "initial_state": {}
}
```

`GET /shadow-candidates` 和 `GET /shadow-candidates/{id}` 查询候选，`POST /shadow-candidates/{id}/stop` 使用 `{ "expected_version": 3 }` 停止后续输入采集。管理版本会随新观测和已完成运行更新；页面执行停止或替换前应取得当前详情。候选状态包含 `active`、`stopped` 和 `budget_exceeded`，预算耗尽时 `error` 提示通过新 epoch 继续分析。

实时观测由现有持久 `engine` 与 `strategy` 投递入口传播，采集步骤在迟到和新鲜度判断之前完成。服务端按候选 epoch、观测逻辑身份和修订号保存内容摘要；相同观测的重复投递沿用已有任务，同一身份及修订内容冲突会保留原投递供核查。正式补算内部递归使用自己的运行身份，其正式派生修订在提交后通过原有持久投递流程进入影子计算。

`generation` 表示已采集的输入代次，`completed_generation` 和 `last_run_id` 表示已完成的最新运行。普通新观测建立一个单点运行，`parent_run_id` 指向上一已创建代次。前序尚未完成时使用 `waiting_parent`，任务等待后重新检查；前序完成后，服务端把实际初始状态保存为新的执行快照，`initial_state_resolved=true`。`captured_snapshot_id` 保留入口时的快照身份，`snapshot_id` 指向解析前序状态后的实际计算快照，重试继续使用该快照。迟到或修订观测使用当前 epoch 保存的有效输入序列重新计算，再为后续新观测提供状态。

影子结果通过 `formal_input_id` 关联正式求值回执。`formal_status=realtime_evaluation` 表示实时正式计算；`historical_recompute_projection` 表示正式补算已提交的求值及派生修订，其中 `formal_evaluation.run_id` 提供补算身份。`formal_outputs` 提供当前保存的正式派生值及修订信息，`formal_difference` 比较该求值与候选求值。尚未找到对应回执时显示 `no_corresponding_formal_evaluation`，页面展示“暂无对应正式求值”。补算与实时回执按各自身份保留，查询时使用当前对应的正式版本。

所有分析结果和候选状态都由独立记录保存；正式节点状态、告警、投影任务与设备动作继续由正式流程提交。取消独立分析保留已提交结果并停止后续计算，重试使用相同运行和任务身份。停止候选保留已经创建的分析运行，页面可以继续查看或按任务入口取消这些运行。

## 可运行的页面夹具

仓库中的 `Platform/cmd/sf-history-fixture` 会建立新的隔离 SQLite 数据目录，准备已发布计划与独立分析运行，再启动实际 `app.Application` 和后台任务。实时样本通过 `Store.Ingest` 进入持久投递，迟到数据由正式 River 补算任务处理。启动成功前，夹具会核对第三条有效派生输入的候选值为 61、正式值为 60、观测修订为 2；服务使用本机回环地址。

1. 在 `Platform` 目录执行 `go run ./cmd/sf-history-fixture -dir /private/tmp/sf-history-browser-demo -address 127.0.0.1:18094`，为 `-dir` 选择尚未存在的目录。需要直接提供已构建页面时，增加 `-static` 并填写前端构建目录；需要有限时长验证时，增加 `-duration 10s`
2. 等待终端出现 `History fixture ready`，从数据目录的 `private-credentials.json` 读取本次生成的登录信息，用页面正常登录。该文件只用于本机夹具，截图、前端测试结果和交付报告使用其余公开样例文件
3. 打开 `fixture-replay-assets` 查看版本 1、2 与资产 Line A、Line B 的变化，第一条计算值为 9007199254740994；`fixture-compare-changed` 的左侧窗口值依次为 9007199254740996、9007199254740998、7，右侧各增加 1，`fixture-compare-unchanged` 的两侧行为一致
4. 打开 `shadow:fixture-live:1:4` 查看实际迟到补算产生的派生修订、正式补算身份与候选差异；`shadow:fixture-failure:1:1` 提供计算失败，下一代 `shadow:fixture-failure:1:2` 提供 `waiting_parent`，`fixture-cancelled` 提供可重试的取消任务
5. 使用数据目录的 `manifest.json` 查看运行身份和预期结果，`http-samples.json` 保存运行、快照、步骤、候选和任务的实际 HTTP 响应。通过 Ctrl+C 结束服务，数据库与样例仍保留在该目录；重新准备样例时使用新的目录

普通影子输入的每份运行包含一个新观测，候选上下文保留各设备字段的最新值，以及规则窗口内满足 `observed_ms > newest_ms - window_ms` 的观测。窗口为零的单设备、单字段持续样本，上下文最多一条；乱序或修订输入会读取当前 epoch 的有效输入序列，其总数受 10000 条预算限制。
