AI 协议工程师交接检查点

范围对应 E2-10 官方 MCP SDK 与 E2-11 模型 API 适配。主协调者已准备下面的实现与第一轮验证；依据用户更新的职责安排，后续实现修复和完整验证由 AI 协议工程师负责。当前状态为实施中，尚未更新为验收通过。

已修改 `Platform/internal/ai/mcp.go`，使用官方 `github.com/modelcontextprotocol/go-sdk/mcp` v1.8.0、无状态 Streamable HTTP 和 JSON 响应。每个 HTTP 请求重新认证 AI 专用身份、检查读取与草稿权限，再选择只读或草稿工具集；服务实例仅缓存工具元数据，业务调用继续通过 API 到统一应用用例。旧客户端缺少 Accept/Content-Type 时填入现有 JSON 约定；显式协议和内容类型由 SDK 检查。GET/DELETE 按无状态协议返回 405，跨站 Origin 拒绝。

新增 `Platform/internal/ai/mcp_sdk_test.go`，通过官方客户端实际 HTTP 调用检查 2025-03-26、2025-06-18、2025-11-25、2026-07-28 的协商、发现、查询、保存草稿、版本冲突及退出登录后撤权。`ai_test.go` 中旧 JSON-RPC 客户端的断言改为解析协议结果：SDK 对不存在的工具返回协议错误，成功结果可省略 isError=false。原有三类草稿与页面助手回归仍保留。

新增 `provider.go`、`provider_chat.go`、`provider_responses.go`、`provider_test.go`，重构 `chat.go`。内部完整消息 `ModelMessage` 包含内容块、工具调用及 Responses 原生输出项；`StreamEvent` 独立描述流式分片。明确选择 provider（openai 或 openai-compatible）和 api（chat_completions 或 responses），stream 可选，旧配置缺省使用兼容 Chat Completions 流式调用。Responses 手工保留 reasoning/encrypted_content、function_call 和匹配 call_id 的输出，store=false；工具只在取得完整成功响应后执行。测试模拟端点覆盖两种 API 的流式/非流式、原生项续传、大整数、截断、错误结束、参数前后不一致、CRLF/多行 SSE 和错误分类。没有调用外部付费模型服务。

`Platform/internal/configcenter/policy.go` 已允许 ai.model 的 provider/api/stream 字段；`app.go` 中 `SF_MODEL_PROVIDER`、`SF_MODEL_API` 默认字段由架构工程师补充。依赖已由工具链工程师取得并写入 Platform/go.mod/go.sum，后续修改需要先协调唯一写入者。

最近一次 `go test ./internal/ai -count=1` 退出 0，用时 1.658 秒，发生在规则计算核进一步修改之前。共享 Go 环境：GOMODCACHE=/private/tmp/smartfactory-evolution-gomodcache、GOCACHE=/private/tmp/smartfactory-evolution-go-cache、GOPATH=/private/tmp/smartfactory-evolution-gopath、GOTOOLCHAIN=local。相关执行结果尚未保存为完整 JSON 验证报告。

后续流程：

1. 检查所有新增代码与当前整体编译状态，修复协议适配发现的问题；保持权限、精度、工具参数与完整结束条件
2. 处理旧实例已经保存的 ai.model 参数 Schema 升级：启动目前只创建缺失参数，更新 AdditionalDefaults 不会自动改写已有 Schema；保留既有加密值、程序归属、应用状态和版本语义，并形成实际旧实例测试
3. 为兼容清单补充实际支持的 MCP 与模型 API 版本，并使 SDK 支持列表与清单一致；与规则工程师协调 manifest.json 和契约生成，避免覆盖 compiled_plan 格式
4. 增加 Responses 模拟端点到页面助手 HTTP 工具循环的完整业务验证；现有四组合测试覆盖 modelCompletion 与手工续传，旧页面助手回归使用 Chat Completions
5. 检查完整内容块、错误结束和 SSE 预算；必要时修复完整消息回传时的内容块丢失或不支持类型的处理，清理仅为编译占位的错误文字
6. 完成适用 race、已有三类草稿回归和配置测试，生成带源文件摘要、命令、实际通过数量与失败修复记录的 `.local/evolution/ai-protocols-verified.json`，报告剩余事项并交回主协调者验收

参考来源为官方 [MCP Go SDK v1.8.0](https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.8.0)、[协议文档](https://github.com/modelcontextprotocol/go-sdk/blob/v1.8.0/docs/protocol.md)、[OpenAI 流式工具调用](https://developers.openai.com/api/docs/guides/function-calling#streaming) 与 [Responses 迁移说明](https://developers.openai.com/api/docs/guides/migrate-to-responses)。已通过 OpenAI Docs 技能读取当前官方内容。未来 E4-07 的 AI 依据记录与页面尚未实施，应单独分配同专家角色的后续任务。
