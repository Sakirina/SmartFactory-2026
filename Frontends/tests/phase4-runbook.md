# 第四阶段浏览器复验

每次复验使用新的私有目录与 SQLite 数据库，业务、AI 和精度服务分别启动。前端版本由最终输入清单固定，后端采用交付的固定源码或二进制。`phase4-browser.mjs` 按业务、补充流程、查询状态、精度、尺寸和 AI 的次序执行，每组保存独立日志、结果及错误页面上下文；业务补充读取同一轮核心流程生成的交接和模板记录，状态组在组内读取刚保存的 F01 观测。

以下示例使用独立审查的 19510 至 19514 端口。前端目录是当前命令目录，`SF_P4_BIN` 指向固定后端构建的二进制目录，`SF_P4_STATIC` 指向本次生产构建目录。使用 Node 24.21.0、npm 11.19.0，并保持既定 Go 工具链环境。

1. 安装锁定依赖并构建前端，选择尚不存在的运行目录

   ```sh
   export SF_PHASE4_RUNTIME="/private/tmp/smartfactory-phase4-review-独立身份"
   export SF_PHASE4_REPORT_DIR="$SF_PHASE4_RUNTIME/reports"
   export SF_P4_BIN="/private/tmp/固定后端/bin"
   export SF_P4_STATIC="$SF_PHASE4_RUNTIME/dist"
   npm ci --cache "$SF_PHASE4_RUNTIME/npm-cache"
   SF_BUILD_DIR="$SF_P4_STATIC" npm run build
   npm run typecheck:compat
   npm run test:unit
   ```

2. 在各自持续运行的终端或执行会话启动业务云端、现场与 AI 服务

   ```sh
   "$SF_P4_BIN/sf-business-fixture" -dir "$SF_PHASE4_RUNTIME/cloud-final" -mode cloud -address 127.0.0.1:19510 -static "$SF_P4_STATIC" -duration 2h
   "$SF_P4_BIN/sf-business-fixture" -dir "$SF_PHASE4_RUNTIME/edge-final" -mode edge -address 127.0.0.1:19511 -static "$SF_P4_STATIC" -duration 2h
   "$SF_P4_BIN/sf-ai-evidence-fixture" -dir "$SF_PHASE4_RUNTIME/ai-final" -address 127.0.0.1:19512 -model-api responses -model-stream=true -static "$SF_P4_STATIC" -duration 2h
   ```

   业务夹具持续实际求值，用于确认流程中的告警更新；趋势精度观测由下一步正式 Ingest 初始化。AI 夹具使用 A02 修正后的固定版本，支持 `prepare_original_resource`、`rebind_definition` 和 `delete_original_resource`。本轮交付二进制摘要为 `dcd8a7027e88a221416856a2a53a4e195a11507189e549806061c53e3a54543e`。

3. 使用正式云端、现场入口初始化两套精度服务，再通过正常登录和 Ingest 建立八种观测

   ```sh
   python3 scripts/phase4-fixtures.py serve-precision --dir "$SF_PHASE4_RUNTIME/cloud-precision" --kind cloud --binary "$SF_P4_BIN/sf-cloud" --static "$SF_P4_STATIC" --port 19513
   python3 scripts/phase4-fixtures.py serve-precision --dir "$SF_PHASE4_RUNTIME/edge-precision" --kind edge --binary "$SF_P4_BIN/sf-edge" --static "$SF_P4_STATIC" --port 19514
   ```

   在其他执行会话运行种子命令。

   ```sh
   python3 scripts/phase4-fixtures.py seed-precision --dir "$SF_PHASE4_RUNTIME/cloud-precision"
   python3 scripts/phase4-fixtures.py seed-precision --dir "$SF_PHASE4_RUNTIME/edge-precision"
   ```

   初始化脚本以 `execve` 转入服务进程，随机密码只写入权限 0600 的私有清单，并通过 `SF_BOOTSTRAP_PASSWORD` 传给应用。种子命令将完整 JSON 整数经正式 HTTP 写入，保存实际状态、请求摘要和观测清单。默认展示最近一小时；持续验证超过一小时后，可以再次执行种子命令建立新的当前观测。

4. 设置全部端口并按依赖次序运行浏览器组

   ```sh
   export SF_PHASE4_CLOUD=http://127.0.0.1:19510
   export SF_PHASE4_EDGE=http://127.0.0.1:19511
   export SF_PHASE4_AI=http://127.0.0.1:19512
   export SF_PHASE4_AI_DIR=ai-final
   export SF_PHASE4_PRECISION_CLOUD=http://127.0.0.1:19513
   export SF_PHASE4_PRECISION_EDGE=http://127.0.0.1:19514
   node scripts/phase4-browser.mjs business details state precision surfaces ai
   ```

   业务核心包含五项流程，补充组包含六项，状态组包含 F01、三个 F02 场景及分页范围重置，精度组包含三项，尺寸组遍历三个入口与三种尺寸，AI 组包含八项。单组可以作为脚本参数执行，补充组需要先完成业务核心，分页范围重置需要同组 F01 的产物。重试已经完成工单或删除资源的组时，应启动新的目录并按上述依赖重跑。

5. 核对结果、数据库和审计，再结束本轮服务

   `runner-results.json` 保存各组的起止时间、退出码与统计；截图、脱敏请求原文、控制台和页面错误位于运行目录的 `browser-*` 文件夹。业务核心与补充组的 JSON 记录提供工单、交接、告警操作、模板批次、参数和草稿身份，可在对应 SQLite 中以只读方式核对 `documents`、`sf_business_requests`、`audit` 和 `audit_checkpoints`。AI 原文应与同一夹具的 `model-visible-results.jsonl` 按实际 ToolCallID、完整 UTF-8 内容及摘要核对。

   完成后向本轮的五个应用进程发送 SIGTERM，等待 SQLite 关闭；AI 夹具同时关闭本地模型。精度入口持续运行，需要显式停止。停止前使用进程身份、私有目录和监听端口确认对象。

本机 Chrome 路径由配置明确指定为 `/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`。首次普通沙箱执行曾因 macOS Mach 注册而退出，原始日志已经保留；后续实际页面在获得自动审批的本机浏览器执行环境中通过。独立运行时使用可启动 Chrome 的已授权环境，并保留启动错误与后续结果。
