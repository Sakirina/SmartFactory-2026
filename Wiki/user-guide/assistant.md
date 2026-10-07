# AI 助手与 MCP

[Wiki 首页](../README.md) · [定义编排](definitions.md)

页面助手通过当前人员的身份查询设备、规则、执行和历史对象，具备草稿许可时可以编辑定义。外部 MCP 使用专用 AI 身份调用相同应用服务，工具结果保留资源范围、分页和实际来源，正式定义由平台评审流程采用。

## 配置模型和发送调查

1. 管理员在“配置中心”编辑 `ai.model`，填写 `provider`、`api`、`stream`、端点、模型名、密钥和超时。
2. 供应商选择 `openai` 或 `openai-compatible`，具体 API 选择 `chat_completions` 或 `responses`，按服务端实际协议填写端点；超时允许 1000 至 300000 毫秒，默认 60000 毫秒。
3. 加密保存并核对云端采用报告，再进入“AI 助手”明确设备、字段、时间或规则问题。
4. 阅读回复及工具调用，打开依据抽屉检查实际 JSON、来源版本和摘要，必要时导航到原对象。

本机演示带有模型模拟器，用于检查页面、工具及草稿流程。实际模型调用配置和允许字段见[配置参考](../reference/configuration.md)，供应商与 API 组合由本次配置决定。

## 持久调查和引用

每次发送建立独立调查，保存状态、答案、实际调用和进入模型的工具 JSON 原文，以及资源版本、字节预算与 SHA-256。调查失败、工具空结果、预算超限、引用未知或跨调查引用都保存对应状态，页面据此显示本次依据。

1. 发送后检查状态和工具依据，区分调用失败、正在运行和已结束。
2. 网络断开后重新读取已保存调查，核对终止状态、原因及可访问引用。
3. 查看来源对象时使用当前权限，依据默认保留七天；过期、删除或失去资源授权后，接口清除相应原文、参数与受影响答案。
4. 结束的调查需要清除时提交删除，正文与输入随之清空，调用身份、时间和内容摘要用于审计。

单个工具结果预算为 512 KiB，密钥及授权字段在发送和保存前处理。引用状态和工具原文帮助使用者检查答案与本次实际资料的对应关系。

## MCP 连接

| 入口 | 工具范围 |
| --- | --- |
| `/mcp/read` | 目录、数据、告警、定义、草稿、执行及历史结果读取 |
| `/mcp/draft` | 按账号许可提供保存、校验和模拟草稿 |

为客户端建立启用 AI 标记的专用人员身份，创建时服务将其角色设为 `ai`，再核对所需资源许可。每次 HTTP 请求使用 `Authorization: Bearer <token>`，服务采用官方 Go SDK 的无状态 Streamable HTTP，支持协议协商与 JSON 响应。

仓库客户端的目录查询在本机演示中使用以下命令，程序提示输入密码；HTTPS 部署先使 Python 信任客户 CA，再使用对应地址。

```sh
python3 examples/mcp/client.py discover \
  --base-url http://127.0.0.1:8090 \
  --login assistant \
  --output ./mcp-discovery.json
```

已有会话令牌时，通过 `SF_MCP_TOKEN` 提供并省略 `--login`；密码也可由私有运行环境中的 `SF_MCP_PASSWORD` 提供。示例请求协议版本为 `2025-06-18`，客户端通过初始化协商采用服务支持的版本。

1. 初始化后调用 `tools/list`，取得当前入口实际工具和参数。
2. 使用 `discover_catalogue` 与 `describe_output` 查询字段、单位、版本和资源，按 `has_more`、`next_after` 继续目录页。
3. `query_data` 提供明确设备、测点及起止毫秒时间，单页最多 500 条；后续使用原筛选和 `next_page_token`。
4. 编辑前调用 `describe_definition_schema`，再通过 `save_draft`、`validate_draft`、`simulate_draft` 和 `diff_draft` 检查草稿，由有权限人员在页面完成评审。

## 三类草稿演示

客户端的 `draft-demo` 需要同时具备读取和草稿许可，并能够读取分析、告警和联动三类已发布定义及其设备、分组与依赖。本机演示的 `assistant` 身份具备这些条件，在已经启动的独立演示服务上执行：

```sh
python3 examples/mcp/client.py draft-demo \
  --base-url http://127.0.0.1:8090 \
  --login assistant \
  --output ./mcp-draft-demo.json
```

工具读取目录的全部分页，为三类定义分别创建草稿并修改名称，再校验草稿和查看差异，同时核对正式 API 的授权拒绝结果及运行前后的正式定义摘要。输出中的 `drafts` 保存草稿结果，`denied_api_requests` 保存拒绝记录，两个 `published_*_sha256` 保存正式定义摘要，`passed` 汇总这次检查；运行后可在页面继续阅读所创建的草稿。

本页依据：[模型配置](../../Platform/internal/configcenter/ai_schema.go)、[调查应用](../../Platform/internal/application/investigations.go)、[模型调用](../../Platform/internal/ai/chat.go)、[MCP 服务](../../Platform/internal/ai/mcp.go)、[工具目录](../../Platform/internal/ai/tools.go)、[示例客户端](../../examples/mcp/client.py)。
