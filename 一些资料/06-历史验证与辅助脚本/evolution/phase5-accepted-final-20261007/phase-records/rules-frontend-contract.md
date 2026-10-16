# 第二阶段规则接口

接口使用 `/api/sf/v1` 前缀和现有 Bearer 身份。草稿模拟、节点目录和计划诊断由 `Platform/internal/api/rule_routes.go` 与现有 Huma 注册共同生成契约，前端类型以本轮生成的 `contracts/1.0/api-types.ts` 为准；服务端验证记录见 [规则计算实现与验证](rules-engine.md)，验收状态由主协调者维护。

## 连续模拟

`POST /drafts/{id}/simulate` 使用 `points` 数组提交连续观测。`expected_version` 可指定草稿修订；发生变化时返回 409。响应的 `draft_revision` 记录实际采用的修订，定义版本与计划身份分别来自该修订的定义内容及编译结果。

```json
{
  "expected_version": 3,
  "points": [
    {"id":"input-b","device_id":"counter-1","key":"count","observed_ms":1800000001000,"source_sequence":2,"value":9007199254740993,"quality":"GOOD","unit":"piece"},
    {"id":"input-a","device_id":"counter-1","key":"count","observed_ms":1800000001000,"source_sequence":1,"value":2,"quality":"GOOD","unit":"piece"}
  ],
  "history": [],
  "initial_state": {"counter-1":{"counter":{"count":10,"active":false,"candidate":false,"since_ms":0,"previous":0,"last_ms":1800000000000}}},
  "order": "event_time",
  "clock": {"freshness_ms":5000},
  "timeout_ms":5000
}
```

`initial_state` 按计算实体、节点 ID 两层组织。选择器配置 `asset_id` 时，该资产是计算实体；其余情况使用设备 ID。每个实体独立延续节点状态。输入对象保留原数值、质量、单位、来源和时间；JSON 数值需要沿用前端的精确数值解析与编码，示例中的大整数须完整保存。

`order` 默认 `event_time`，依次按照 `observed_ms`、`source_sequence`、`id` 升序排列，三个字段完全相同时保持原数组顺序。`provided` 按请求数组顺序处理，响应的 `input_index` 指向原数组位置。未提供观测 ID 时，入口按照原数组下标分配 `simulation:<index>`；重复 ID 返回输入错误。

每个点的默认逻辑时刻和新鲜度参照时刻都采用其 `observed_ms`。`clock.at_ms` 指定第一个执行点的逻辑时刻，`step_ms` 按执行顺序递增；`freshness_at_ms` 可独立指定新鲜度参照时刻，并使用同一递增量。`freshness_ms` 覆盖计算的新鲜度阈值；省略后先采用规则参数，再采用设备采样间隔的三倍与 5000 ms 中的较大值。未来观测相对于新鲜度参照时刻返回 `fresh=false`。每个响应结果都提供实际的 `clock_ms`、`freshness_at_ms` 和 `freshness_ms`。

明确传入 `history: []` 使用空历史；省略 `history` 时，入口读取所需的数据库历史，并把实际快照作为 `history` 返回。`history_source` 记录 `provided` 或 `database_snapshot`，`history_sha256` 覆盖返回的历史内容。此前已执行的序列输入加入后续点的窗口，窗口采用 `(observed_ms - window_ms, observed_ms]`；不设窗口时只使用当前观测。`input_sha256` 覆盖计划身份、采用的排序、时钟、规范化输入、历史、初始状态和资产版本。

`asset_versions` 可提供明确的资产快照数组，每项包含 `id`、`version`、`effective_ms`、`parent_id`、`sampling_ms`；省略时读取有关设备和祖先资产的版本历史。层级归属以观测时刻之前最新版本判断。调用者需要拥有输入、历史、初始状态实体、资产及父级引用的读取权限。

响应包含 `plan_id`、`plan_sha256`、`content_sha256`、`draft_revision`、`order`、`clock`、`timeout_ms`、`input_sha256`、`history_sha256`、`history_source`、`history`、`asset_versions`、`results` 和 `final_state`。`results` 的每项包含 `input_index`、原始 `point`、实际时钟以及 `evaluation`；求值结果中的 `trace` 按节点记录输入、输出、前后状态、跳过状态与错误。`final_state` 使用与 `initial_state` 相同的实体和节点结构，可以作为下一次模拟的初始状态。

单次请求最多 1000 个点、40000 条历史和 40000 项资产快照，整体时间预算默认及上限均为 5000 ms。`timeout_ms` 可减少本次预算，规则的 `policy.timeout_ms` 继续限制单次图计算；取消信号传递到准备、查询和计算。模拟过程只读取业务数据，所有节点状态保存在本次响应中。

原有 `{ "point": {...} }` 请求仍返回单个 `Evaluation`。`point` 与 `points` 同时提供时返回输入错误，旧请求也可附带 `expected_version`。

## 节点目录

`GET /rule-nodes` 返回 12 种内建节点。每项提供 `type`、`label`、适用定义 `kinds`、`inputs`、`outputs`、`parameters`、`parameter_schema`、`stateful` 和 `description`。参数具有类型、必填条件、默认值、枚举、数值范围、单位和帮助说明，`parameter_schema` 由同一目录产生。编辑器可以依据这些字段生成基础表单，提交发布时仍使用后端的图、端口、表达式和引用检查。

## 计划诊断

`GET /definitions/{id}/plans` 返回已发布历史版本的计划状态，包含定义版本、内容摘要、计划身份、计划摘要和 `prepared`、`unprepared` 或 `isolated` 状态。隔离项的 `issue` 提供错误代码、错误内容和记录时刻。服务端逐项检查历史定义及引用资源权限，适合在规则详情中展示旧版本无法准备的原因。
