# 开发与扩展说明

## 构建与验证

仓库包含两个 Go 模块：`DataTransfer` 和 `Platform`。后者通过本地 replace 引用采集协议类型，构建时保留相邻目录结构。Go 模块声明最低版本 1.25，本轮本机构建使用 Go 1.27.0；前端版本由 `Frontends/package-lock.json` 固定。

1. 安装符合版本要求的 Go、Node.js、Python 3 和 Docker Compose，按 `deploy/images.lock.json` 核对需要的镜像。依赖准备好后，分别进入 `DataTransfer` 与 `Platform` 执行 `go test ./...`。
2. 在 `Frontends` 执行 `npm ci`、`npm run build`。浏览器验证使用 `npm run test:e2e`，通过 `SF_BROWSER_EXECUTABLE` 指定 Chromium 路径，默认业务地址为 `http://127.0.0.1:8090`。
3. 在仓库根目录执行 `python3 scripts/build-release.py --output /tmp/smartfactory-release --os linux --arch amd64`。构建器输出 Linux 二进制、前端静态文件、源码快照、契约、脚本、文档、组件许可证和逐文件摘要。
4. 执行 `python3 scripts/verify-release.py /tmp/smartfactory-release` 检查交付包。发布清单同时记录源码提交和未提交工作树，验收报告中的二进制摘要用于识别实际受测构建。

从交付包重新构建时，进入 `source` 目录后执行上述过程；源码快照保留两个 Go 模块及相邻路径，构建器支持没有 Git 元数据的交付源码。契约验证依赖 `tests/fixtures/requirements-contracts.txt`，实际 OPC-UA 协议测试依赖 `tests/fixtures/requirements-opcua.txt`；这些 Python 验收依赖安装到单独的虚拟环境后运行。

Go 构建缓存、下载缓存和验收数据可分别指定目录。批量测试优先使用 `:memory:` 或受容量限制的 PostgreSQL tmpfs；持久提交、重启和备份测试使用独立的小规模文件夹，保留具体数据规模与磁盘写入测量。

## 产品材料的编辑与生成

`deliverables` 中的 DOCX 和 PPTX 可在 Office 或兼容软件中编辑。`scripts/create-product-document.py` 使用 `python-docx` 从当前说明与 `docs/evidence/screenshots` 生成文档；执行前准备支持中文的字体。文档中的表格和正文保持可编辑，实际页面截图以图片插入。

`scripts/create-product-presentation.mjs` 使用 Codex 工作区提供的 `@oai/artifact-tool` 与演示文稿验证工具。运行前设置 `RUNTIME_PYTHON` 为工作区 Python 可执行文件，`RUNTIME_NODE_MODULES` 为附带的 Node 模块目录，`SF_PRESENTATIONS_SKILL` 为 Presentations 技能目录；随后使用工作区 Node 执行该脚本。其他系统可通过 `SF_PRESENTATION_FONT` 和 `SF_PRESENTATION_FONT_FILES` 指定字体家族及字体文件，多个文件使用系统路径分隔符连接。生成器保留原有 PPTX 到 `.local/materials-build`，检查原生表格、图表、嵌入工作簿与文件关系后写入交付目录。

每次更新材料后，渲染所有文档页和幻灯片，检查中文、表格、图表、截图和换页，并核对数字对应的 JSON 证据。演示视频由 `scripts/record-demo.mjs` 驱动实际页面和模拟设备，记录动作断言，字幕文件与 MP4 一同交付；生成工具的具体环境参数见各脚本顶部。

## 公共契约

`contracts/1.0` 包含 JSON Schema 与 OpenAPI 3.1，采集 gRPC 定义位于 `DataTransfer/proto`。从 `Platform` 执行 `go run ./cmd/sf-contracts` 可依据模型和路由重新导出契约及 24 个可编辑工厂定义；`scripts/check-contracts.py` 检查结构和引用。

业务 API 使用 `/api/sf/v1`，登录返回 Bearer token。实体、草稿、看板、配置等更新传入 `expected_version`，冲突返回 HTTP 409；动作权限和每个引用资源均在服务端检查。大整数通过前端精确 JSON 解析保留，图形坐标在展示阶段按需要转为数值。

查询使用 `device_ids`、`keys`、`from_ms`、`to_ms`、`resolution` 与 `limit`；SSE `/events` 支持 `window_ms`，每次数据事件返回统一 `DataResult`。指定 `include_latest=true` 可同时取得授权系列的最新值，供指标卡片使用，历史窗口继续控制 `points`。结果同时提供 `quality`、`sources`、`completeness`、`backfill`、`revisions` 和 `gaps`，调用者应随数值展示这些状态。

三类定义使用同一 `Definition`。开发新节点时，在 `internal/engine/definitions.go` 登记端口、类型和允许的定义种类，在执行器实现行为，同时验证实时与历史路径。带副作用的设备动作由边缘控制服务执行，分析补算保持数据处理语义。

## 协议采集插件

`DataTransfer/pkg/plugin` 提供独立进程 SDK，`examples/http-plugin/main.go` 为可执行示例。主程序创建受限目录中的 Unix socket，通过 `SF_PLUGIN_SOCKET` 传入地址，再使用 `Init`、`Start`、命令和配置 RPC 与插件通信。主程序检查插件输出的连接器和设备身份，插件退出后其他连接器继续采集。

示例插件从协议模拟器的 HTTP 状态接口采集读数，并将控制请求转换为设备状态修改。运行 `go build -o /absolute/path/http-plugin ./examples/http-plugin` 后，在连接器配置中使用 `protocol: process` 与 `process.executable` 的绝对路径，`connection.url` 指向本机模拟器；具体请求和消息字段以示例源码及 protobuf 为准。

设备事件和命令回执必须保留稳定消息 ID。返回相同 ID 时保持内容一致，程序在持久保存后才发出业务确认。浮点、整数、布尔和二进制值使用相应 `DataValue` 分支，质量与工艺阈值分别表达。

## 组织同步插件

`Platform/cmd/sf-hr-plugin` 实现组织拉取示例。平台以 JSON 标准输入传入 `method`、`source`、`after`、配置和解密后的本次调用凭据，插件通过标准输出返回 `OrganizationSync`；凭据在配置中心加密保存，插件配置中使用条目标识。

插件支持增量与全量序列，推送接口在组织数据和游标提交后确认。序列冲突、内容变化和凭据不符会被拒绝，进程崩溃或超时显示为插件状态。人员停用、组织移动和授权调整影响后续审批与通知，历史审计保存原身份快照。

## 页面助手与 MCP

页面助手使用兼容 Chat Completions 的模型端点，参数条目为 `ai.model`，包含端点、模型、密钥和超时；页面通过流式结果显示工具调用、草稿与错误。`sf-model-mock` 提供固定响应，便于在无外部模型服务时复验。

MCP 提供 `/mcp/read` 和 `/mcp/draft` 两个入口，同一 AI 身份使用相同资源权限。发现过程依次读取目录、字段结构、范围和数据，草稿工具支持三种定义的创建、修改、校验、解释和差异查看。正式发布和设备控制需要人工身份完成。

外部客户端示例为 `examples/mcp/client.py`。运行 `python3 examples/mcp/client.py discover` 查看目录，`draft-demo` 创建和修改三类草稿并核对正式版本摘要；登录信息通过提示或受控环境传入，示例不依赖第三方 Python 包。
