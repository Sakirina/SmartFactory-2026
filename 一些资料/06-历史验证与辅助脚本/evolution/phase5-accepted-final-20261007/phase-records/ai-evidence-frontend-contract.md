# AI 调查与依据前端契约

每次 `POST /api/sf/v1/assistant` 建立独立调查，请求仍采用 `{"messages":[{"role":"user","content":"调查内容"}]}`。客户端提交 1 至 100 条用户或助手文本，服务端保存当前用户、输入、模型选择、实际工具调用、提供给模型的 JSON 原文以及最终回答。依据默认七天内可访问，过期后详情返回 410，调查响应清空原文、参数、资源及保存的回答。过期判断发生在读取时，持久内容由显式删除操作清空；时间采用 UTC 毫秒。

`GET /api/sf/v1/investigations?limit=20&after=身份` 返回当前用户的 `InvestigationList`，包含 `items`、`has_more` 和可选的 `next_after`；`GET /api/sf/v1/investigations/{id}` 返回 `Investigation`。列表省略输入、依据原文及完整答案，详情提供输入、答案和依据摘要。其他用户的调查返回 404，已经结束的调查可通过 `DELETE /api/sf/v1/investigations/{id}` 清空持久保存的输入、答案、依据原文、参数和资源，保留调用身份、时间及内容摘要用于审计，正在运行的调查返回 409。

前端按以下流程处理 SSE，每个事件采用 `data: JSON`，事件类型位于 JSON 的 `type` 字段：

1. `investigation` 提供 `investigation_id` 和详情 `url`，页面保存身份并显示运行状态。
2. `tool` 提供工具 `name`、实际 `tool_call_id` 和调查身份；`evidence` 提供 `evidence_id`、状态、原文字节数、512 KiB 预算及 `visible_sha256`。工具结果保存为 `prepared`，后续模型请求成功完成后成为 `delivered`。
3. `delta` 提供 `text`、调查身份及 `provisional=true`，页面逐步显示生成中的文字。
4. `final` 提供最终 `answer`、`evidence_status`、逐条 `references` 和依据摘要 `evidence`，页面以此更新完整答案和引用状态；`done` 确认本轮结束。
5. `error` 提供调查身份、失败信息及 `evidence_status=unsupported`。网络断开后读取调查详情，使用保存的 `status`、`error` 和依据状态恢复显示。

`evidence_status` 描述引用可用情况，页面可采用以下文案：

| 状态 | 页面文案 |
| --- | --- |
| `pending` | 正在取得与检查依据 |
| `supported` | 所有引用均可访问 |
| `partial` | 部分引用当前可访问 |
| `unsupported` | 当前引用均不可用 |
| `missing` | 回答尚未引用依据 |

模型引用格式为 `[evidence:依据身份]`。服务端对每条引用检查同一调查归属、工具结果是否已提供给模型、内容是否存在、保留期限以及原始资源集合的当前权限。`valid` 对应可访问引用；其他状态为 `unknown`、`cross_investigation`、`expired`、`deleted`、`forbidden`、`tool_error`、`budget_exceeded`、`empty`、`not_delivered`，分别对应未知身份、其他调查、期限结束、内容或资源删除、当前资源授权失效、工具失败、结果超过预算、空结果和工具结果尚未提供给模型。

引用详情采用 `GET /api/sf/v1/investigations/{id}/evidence/{evidence_id}`，返回 `InvestigationEvidence` 的 `visible_content`、调用参数、`resources`、版本、预算和摘要。`visible_content` 为进入模型工具消息的原始 JSON 字符串，`visible_sha256` 按其 UTF-8 字节计算。调查详情每次重新检查原始资源；设备授权失效、原始依赖删除或依据过期后，响应清空相关参数、资源和保存的回答，依据详情分别返回 403、404、410。旧定义随后移除设备或依赖时，原依据仍保留当时的资源集合与版本，并按该集合检查当前授权。

单个模型工具结果最多 524288 字节，包含 `_evidence` 的最终 JSON 也遵守该限制。超过预算时，工具结果替换为错误对象，`status=budget_exceeded`，依据保存这个模型实际取得的错误对象及原结果字节数。模型密钥、授权头和凭据字段在发送与持久化之前处理为 `[redacted]`。结果中的 `_resources` 提供该次授权读取涉及的资源及版本，页面可以结合 `kind`、`id` 和返回的 `url` 展示来源导航。

目录、定义和草稿工具采用 `AIDocumentPage`：`items`、`scope`、`has_more`、`next_after`、`scan_budget`、`budget_exhausted`。每页最多返回 200 个授权对象，候选扫描预算为 1000；权限过滤后可能返回空页并带有续页身份。续页身份经过加密，绑定用户、集合、类别和十五分钟期限。趋势、告警及执行工具采用正式 `QueryPage`，依据原文保留筛选范围、快照游标、分页条件、质量、来源和修订信息。

资源身份支持中文、斜杠和冒号。导航时用 `encodeURIComponent` 转义完整身份路径段，依据原文按字符串展示；结构化数据沿用前端精确 JSON 解析和序列化函数。所有类型及路由随组合源码由 `sf-contracts` 和正式 `openapi-typescript` 生成，`post_assistant` 操作共用 `AssistantRequest` 与 `AssistantEvent`，SSE 响应类型为 `text/event-stream`。本轮组合生成 157 份独立 JSON Schema，OpenAPI 包含 158 个模型和 96 个操作；生成记录、固定源码、可复用夹具二进制及其摘要保存于 `.local/evolution/ai-evidence-verified.json`。

## 隔离浏览器夹具

`Platform/cmd/sf-ai-evidence-fixture` 使用新的 SQLite 目录，复用业务夹具并装配正式调查路由、助手、MCP、本地模型、实际执行申请和完成的历史回放。本地模型同时支持 Chat Completions、Responses 及流式、非流式请求，默认使用 Responses 流式。实际执行申请保持待审批状态，历史回放通过正式应用用例执行。

启动流程如下：

1. 在 `Platform` 中使用既定 Go 环境执行 `go build -p 1 -o /private/tmp/sf-ai-evidence-fixture ./cmd/sf-ai-evidence-fixture`。
2. 执行 `/private/tmp/sf-ai-evidence-fixture -dir /private/tmp/sf-ai-evidence-browser-新身份 -address 127.0.0.1:0 -model-api responses -model-stream=true`。每次选择新的目录，应用和模型均仅监听本机；可添加 `-static 前端构建目录` 和 `-duration 30m`。
3. 从生成的 `manifest.json` 读取应用地址和资源身份，从同目录的 `private-credentials.json` 读取 `operator-a`、`operator-b` 及随机密码。私有清单权限为 0600，包含本地模型配置；页面使用正常登录端点取得用户令牌。
4. 输入“调查温度、执行与历史依据”取得定义、趋势、执行详情、历史回放和步骤依据；输入“读取身份 调查/温度:一号”验证中文、斜杠、冒号身份与引用导航。
5. 输入“伪造引用”“跨调查引用”“空查询”“查询失败”“超预算”“无依据回答”“模拟中断”“模拟服务错误”或“模拟超时”，分别验证状态。跨调查场景在一次正常调查之后执行，本地模型会引用上次工具依据。
6. 使用 `operator-a` 的令牌向夹具专用 `POST /__fixture/state` 提交 `{"action":"revoke"}` 或 `{"action":"restore"}` 改变该账号的资源授权；`{"action":"expire","investigation_id":"已保存调查身份"}` 将该调查及依据设置为过期；`{"action":"delete_resource"}` 删除夹具温度定义。刷新原调查与依据详情，验证当前状态及内容清空。删除资源后用新夹具目录继续其他流程。
7. 夹具的 `model-visible-results.jsonl` 保存本地模型实际读取的工具原文、ToolCallID 和 SHA256，可与引用详情逐字节核对。结束验证后发送 SIGTERM 或等待指定时长，应用、模型及数据库连接会关闭。

模型工具循环的协议依据见 [OpenAI 工具调用文档](https://developers.openai.com/api/docs/guides/function-calling) 与 [Responses 流式事件文档](https://developers.openai.com/api/docs/guides/streaming-responses)，本项目保留匹配的调用身份及完整结束事件。
