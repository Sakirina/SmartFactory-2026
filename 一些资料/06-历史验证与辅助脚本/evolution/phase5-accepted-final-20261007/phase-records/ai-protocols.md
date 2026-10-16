SmartFactory AI 协议与模型 API 验证记录

本记录对应第二阶段 E2-10、E2-11。实现由 `Platform/internal/ai` 承载，旧模型参数升级位于 `Platform/internal/configcenter/ai_schema.go`，启动调用位于 `Platform/internal/app/app.go`。受测源码、实际命令、退出码与测试数量保存在 [AI 协议验证证据](../../.local/evolution/ai-protocols-verified.json)；本角色交付完成后，由主协调者复核逐项验收状态。

MCP 使用官方 Go SDK v1.8.0，通过无状态 Streamable HTTP 提供 `/mcp/read` 与 `/mcp/draft`。当前 SDK 与正式清单共同支持 `2026-07-28`、`2025-11-25`、`2025-06-18`、`2025-03-26`、`2024-11-05`，当前协议为 `2026-07-28`；读取和写入清单使用相同的五个版本。测试使用官方客户端逐个协商版本并实际发现工具、查询目录、保存草稿、验证修订冲突和退出登录后的撤权，既有 JSON-RPC 客户端继续通过原有三类草稿回归。支持范围直接依据 SDK 的 [SupportedProtocolVersions 实现](https://github.com/modelcontextprotocol/go-sdk/blob/v1.8.0/mcp/shared.go) 和 [Streamable HTTP 实现](https://github.com/modelcontextprotocol/go-sdk/blob/v1.8.0/mcp/streamable.go) 核对。

每个 MCP HTTP 请求都会重新认证 AI 专用身份，并检查读取权限、草稿权限和具体资源权限；缓存仅包含工具目录。官方客户端保持连接期间，资源权限被撤销、身份被禁用或登录令牌退出后，后续调用会按当前身份状态处理。缺少 Accept、Content-Type 的既有客户端使用 JSON 约定，显式媒体类型和协议版本由 SDK 校验。跨站 Origin 返回 403，无状态 GET、DELETE 返回 405，请求正文超过 2 MiB 返回 413；通知请求按照协议返回 202，服务没有生成持久 MCP 会话。

模型适配通过 `provider` 与 `api` 两个字段明确选择调用方式。供应商枚举为 `openai`、`openai-compatible`，API 枚举为 `responses`、`chat_completions`；两种供应商使用所选 API 的对应消息格式。端点填写基础 API 地址，适配器分别添加 `/responses`、`/chat/completions`。`stream` 可以选择流式或非流式调用；已有参数没有这些字段时，使用 `openai-compatible`、Chat Completions 和流式调用。正式兼容清单登记两种 API，当前格式为 `responses`。

内部 `ModelMessage` 保存完整内容块、工具调用与调用标识，`StreamEvent` 独立描述分片。Chat Completions 的非流式内容块保存类型与原始载荷，后续请求继续传递块内信息和精确整数；流式正文、refusal 和工具参数分别重组，并核对成功 finish_reason 与实际工具调用是否一致。Responses 保存完整原生输出项，包括 reasoning、encrypted_content、消息块、function_call 及其 call_id，随后将对应工具结果作为 function_call_output 发送。请求采用 `store=false` 并请求 reasoning.encrypted_content，续传规则依据 OpenAI Docs 的 [Responses 迁移说明](https://developers.openai.com/api/docs/guides/migrate-to-responses) 与 [流式工具调用说明](https://developers.openai.com/api/docs/guides/function-calling#streaming) 实现。

Responses 的文字分片按 output_index、content_index 和 item_id 关联，函数参数按输出位置和函数项身份关联；response.created、分片中的 response_id 与最终响应也要保持一致。CRLF、多行 SSE 与分段 HTTP 字节传输具有实际测试。流式函数参数、arguments.done、output_item.done 和 response.completed 中的参数及身份出现矛盾时，适配器返回错误；截断、failed、incomplete、非法完成状态、重复工具标识、重复 JSON 字段和不完整参数同样会被拒绝。参数解析使用精确 JSON 数字，并在形成映射前检查重复字段。

工具只在模型响应完整、结束成功且请求仍然有效时执行。取消和超时会贯穿模型 HTTP 请求与业务调用，已经缓冲的成功响应也会在取消检查中退出，错误结果保留可查询的取消原因。HTTP 429、5xx 被标记为可以重新尝试，工具循环由调用方明确控制。现有工具继续经平台 API 调用统一应用用例，正式发布、设备控制和凭据读取权限由平台身份层执行。

调用预算为：页面消息请求 256 KiB、1 至 100 条消息，模型请求 4 MiB，模型响应与 SSE 总字节数 8 MiB，单条 SSE 行 1 MiB，完整消息与 Responses 流式的可见文字合计 1 MiB，Chat Completions 流式的正文和 refusal 各 1 MiB，完整输出项与单个消息内容块各最多 128 个，每轮最多 16 个工具调用，单个工具参数 256 KiB，参数嵌套最多 32 层，单个工具结果进入模型上下文前最多 512 KiB。页面助手最多执行八轮模型调用，配置超时上限为 300000 ms；预算、取消、超时和错误结束都有实际验证。

已有 `ai.model` 使用原始内置 Schema 时，权威配置节点在默认初始化期间创建新的配置修订，为 provider、api、stream 增加校验规则。升级保留原密文、Value、Program、Effective 与 Applications，配置版本和文档修订分别递增；新修订进入 pending，静态参数进入 restart_required，既有应用记录继续指向原修订。历史文档与 parameter_receipt 保存原内容，新修订建立独立的 id:version 回执；脱敏审计与 config_update 与参数变更共同提交。自定义 Schema 按原有约束继续使用，配置从属节点通过权威订阅取得新修订。

旧实例验证使用从源码基点提取的完整 SQLite 表结构，并填入已经保存的加密模型参数、文档历史、应用状态、历史回执、审计、待发送配置更新和精确观测。测试检查配置版本 7、文档修订 12 在启动后变为 8、13，第二次启动保持相同结果，随后能够保存新 API 字段为配置版本 9；自定义 Schema、从属节点启动、新版订阅、相同版本内容冲突及旧版确认也分别验证。审计故障注入确认参数、文档历史、审计和待发送更新共同回滚，本角色的旧实例专项使用 SQLite 样本。

Responses 模拟端点已经连接实际 `/api/sf/v1/assistant` HTTP 路由。分析、告警和策略三类草稿分别通过流式与非流式调用，完整执行发现定义、读取定义、保存草稿、校验草稿、查询差异、完成答复的六轮流程；既有已发布定义保存原内容。查询结果与策略草稿参数中的 `9223372036854775807` 保持原数值，函数结果携带匹配 call_id，reasoning/encrypted_content 在后续模型请求中继续存在。截断、错误结束、参数冲突、重复字段和项身份变化没有执行写入；只读权限、资源权限和草稿版本冲突作为工具错误返回下一轮模型，取消请求没有保存草稿，委托会话随后清理。

当前功能测试与 race 检查分别通过 39 个顶层测试、80 个末级测试，记录为各 93 个通过事件，失败和跳过均为零。race 期间唯一发生变化的文件是存储专家的 `Platform/internal/store/river_migration_test.go`，该包的测试没有包含在本次命令中；本角色受测源码保持一致。共享清单与契约完成生成后，兼容与生成器五项测试通过，Schema 检查覆盖 71 个类型、24 个示例定义和 56 项 API 操作。

验证过程曾发现内容块测试对 JSON 字段顺序作了额外假设，断言已调整为核对保留字段及精确数值；共享兼容检查曾因生成文件尚未更新、数据库范围测试仍采用旧版本而失败，存储工程师同步测试并统一生成后已通过。系统 Python 缺少 jsonschema 时，检查切换到本轮已有的临时环境，验证记录保存了实际使用的解释器。本轮模型调用均指向本地模拟端点，实际付费模型端点和 PostgreSQL 上的 AI 参数旧实例样本未在本角色专项中执行；第四阶段 E4-07 的 AI 依据引用交给同专家后续任务继续处理。
