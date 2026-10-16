AI 调查前端契约（实施草拟）

页面继续向 `POST /api/sf/v1/assistant` 提交 `messages`，每次请求建立独立调查。服务端按当前用户保存输入、模型选择、实际工具调用、模型取得的返回内容和最终回答；`GET /api/sf/v1/investigations` 提供当前用户的历史列表，`GET /api/sf/v1/investigations/{id}` 返回调查详情，`GET /api/sf/v1/investigations/{id}/evidence/{evidence_id}` 在检查当前资源权限后返回依据内容。

页面处理流式事件时，按下面的流程组织状态：

1. `investigation` 事件提供调查身份和详情地址，页面保存身份并进入运行状态。
2. `tool` 事件提供工具名称与实际 `tool_call_id`，`evidence` 事件提供依据身份、结果状态、内容摘要和预算。调用结果先保存为 `prepared`，下一轮模型成功完成后成为 `delivered`。
3. `delta` 事件为生成中的文字，带有 `provisional=true`；页面可以逐步显示，引用状态等待最终事件。
4. `final` 事件提供最终回答、`evidence_status`、逐条 `references` 和依据列表。页面使用该事件更新完整答案和引用显示。
5. `done` 确认本次调查完成；`error` 提供调查身份与失败状态。页面在网络断开后读取调查详情，取得已经保存的状态。

模型引用格式为 `[evidence:依据身份]`，服务端按当前调查逐条检查。`valid` 表示同一调查、已经提供给模型、内容存在且当前资源授权有效；`unknown`、`cross_investigation`、`expired`、`deleted`、`forbidden`、`tool_error`、`budget_exceeded`、`empty`、`not_delivered` 分别对应未知身份、其他调查、保留期限结束、内容或资源删除、资源撤权、工具失败、超过结果预算、结果内容为空以及结果尚未提供给模型。页面按这些状态显示说明和可用操作，导航地址使用服务端返回的 `url`。

`evidence_status` 为 `supported`、`partial`、`unsupported`、`missing` 或运行期间的 `pending`，其判定根据实际引用状态。详情读取会再次检查每条依据；资源撤权、删除或保留期限结束时，响应清空相关内容、资源信息与保存的回答。依据原文仅在依据详情端点返回，调查列表与调查详情提供摘要和预算。

单个工具结果进入模型上下文前最多为 512 KiB，包含依据标识的最终 JSON 也遵守该预算。超过预算时，模型取得带有 `budget_exceeded` 状态的错误对象，依据保存同一错误原文及原始字节数。目录、定义和草稿工具提供 `items`、`scope`、`has_more`、`next_after` 与扫描预算；续页标识经过加密并绑定用户与筛选条件，资源授权后的空页可以带有续页标识。趋势、告警和执行工具使用正式 `QueryPage`，保存相同的 `scope`、游标、修订、`has_more` 与元数据。

资源身份支持中文、斜杠和冒号，调用路径通过 `encodeURIComponent` 将整个身份作为一个路径段传递。页面采用生成客户端与精确 JSON 解析，依据原文按字符串呈现；数值导出采用既有精确序列化函数，`visible_sha256` 核对原始 UTF-8 字符串。

隔离夹具将提供一个正式应用端口和本地模拟模型端口，使用独立 SQLite 文件与临时凭据；模拟模型完成查询、规则读取、执行与历史调查，通过两种 API 和流式、非流式组合取得依据。夹具可以按场景产生未知引用、其他调查引用、错误结果、空查询和无引用回答；撤权、删除与过期样本将附有具体运行方法和读取路径。夹具命令、二进制、模型取得的原文及对应源码摘要在正式交付时补充。
