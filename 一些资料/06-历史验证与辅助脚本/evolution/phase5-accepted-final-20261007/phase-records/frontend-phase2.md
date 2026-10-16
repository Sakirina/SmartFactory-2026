第二阶段前端交付：E2-09、E2-12

本阶段将连续模拟和后台任务用于实际页面，同时迁移构建与类型生成工具。云端、现场、大屏继续由同一份前端源码构建，接口类型统一来自 `contracts/1.0/openapi.json`。验证记录位于 `.local/evolution/frontend-phase2-verified.json`，请求与数据库核对记录位于 `.local/evolution/frontend-phase2/`，浏览器截图与复现文件位于 `output/playwright/frontend-phase2/`。

| 组件 | 本阶段版本 | 实际使用方式 |
| --- | --- | --- |
| Node.js | 24.21.0 | 使用仓库 `.local/toolchains` 中的运行时 |
| npm | 11.19.0 | 锁文件安装经过独立 `npm ci` 验证 |
| Vite | 8.1.5 | 使用 Rolldown 配置构建三个 HTML 入口 |
| React 插件 | 6.1.1 | 与 Vite 8 的上游 peer 声明一致 |
| TypeScript 正式检查 | 7.0.2 | `@typescript/native` 指向 `npm:typescript@7.0.2`，脚本显式调用其 `bin/tsc` |
| TypeScript 编译器 API | 6.0.2 | `typescript` 指向 `npm:@typescript/typescript6@6.0.2`，生成器和客户端测试使用同一模块 |
| openapi-typescript | 7.13.0 | 官方 AST 生成接口处理 OpenAPI 3.1 |

[Vite 8.1 发行说明](https://vite.dev/blog/announcing-vite8-1)与[迁移说明](https://vite.dev/guide/migration)用于核对 Node 要求和 Rolldown 配置；[TypeScript 7 发行说明](https://devblogs.microsoft.com/typescript/announcing-typescript-7-0/)提供原生 CLI 与旧编译器 API 并存的包别名用法，[TypeScript 6 说明](https://www.typescriptlang.org/docs/handbook/release-notes/typescript-6-0.html)用于核对兼容 API。

openapi-typescript 7.13.0 的实际上游 peer 范围仍为 `typescript: ^5.x`。本项目针对该包配置 `"openapi-typescript": { "typescript": "$typescript" }`，使用 TypeScript 6.0.2 的兼容 API，并验证真实生成、完整生成类型、客户端精度回归及两套类型检查。官方 TypeScript 6 包装器内部依赖 `@typescript/old: npm:typescript@^6`，因此另外把 `@typescript/old` 覆盖锁定为 `npm:typescript@6.0.2`；生成脚本会核对自己和生成器实际加载的 API 版本。该组合的维护依据是本项目保留的验证结果和上游 peer 声明。

正式检查调用 `node node_modules/@typescript/native/bin/tsc --noEmit`，兼容检查调用 `node node_modules/typescript/bin/tsc6 --noEmit --stableTypeOrdering`。显式路径避免 npm 别名安装时的命令入口覆盖；本机实际执行的原生二进制和 6.0.2 API 模块路径已保存在 `compiler-info.json`。工具链检查同时核对别名目标包名、锁定版本及锁文件顶层声明，普通包仍按原版本规则检查。

契约生成流程如下：

1. 在 `Platform` 目录运行 `go run ./cmd/sf-contracts -out ../contracts/1.0 -api-source internal/api -examples ../examples/factory/definitions.json`，由 Go 导出 JSON Schema 和 OpenAPI
2. 在仓库目录运行 `npm --prefix Frontends run generate:api`，由正式 openapi-typescript 生成 `contracts/1.0/api-types.ts`，同一脚本从该 OpenAPI 生成运行时路由表 `Frontends/src/generated-routes.ts`
3. 运行 `npm --prefix Frontends run check:api`、`test:unit`、`typecheck:compat` 和 `build`，核对生成内容、客户端行为、TypeScript 6 兼容性、TypeScript 7 正式检查及生产构建

Go 导出器已经停止生成 TypeScript。生成类型保留组合 Schema、可空集合、参数、200／202 成功响应以及 401／403／409 错误响应；int64／uint64 由官方 AST 转换接口映射为 `number | string | bigint`。普通接口继续把超出安全范围的整数解析为字符串，连续模拟使用精确解析保留数值来源，再按数值原样导出；字符串身份保持字符串。`api-types.ts` 是唯一类型输出，生成客户端的请求、参数和响应均引用其中的 `operations` 与 `components`。

连续模拟位于定义编辑器的“模拟运行”。页面采用已保存的草稿修订，提供多条观测、输入时序、历史来源、逻辑时钟、初始节点状态和资产版本参数。“由服务取得历史快照”省略请求中的 history；“自行提供历史”提交所填数组，空数组明确表示空历史。结果展示每条输入的原数组位置、单位、质量、时间来源、实际时钟、节点输入输出与前后状态，并显示计划、输入、历史摘要和最终节点状态。复现文件包含归一化输入、实际采用的历史与资产以及完整结果，导出后可以重新调用同一草稿修订的模拟接口。

草稿修订冲突会显示服务的 409，保留编辑名称、输入和参数，并停止继续采用过期修订。点击“读取当前修订并保留编辑内容”取得当前持久修订，再通过“保存并校验当前草稿”选定新的模拟修订。实际验证包含超出安全整数范围的输入、同时间戳来源序号排序、数组时序、两种历史来源以及冲突恢复；模拟前后核对 `documents` 中 kind 为 definition、state、alarm、revision、entity、data_catalog 的35条正式文档，以及 observations、latest、rollups 的摘要；每份数据库证据均附实际查询语句，草稿、任务和审计记录按本轮操作发生变化。

“后台任务”在云端与现场导航中提供分页列表、完整身份查询和详情，历史补算页也提供对应入口。详情展示业务创建版本、River 行号、原投递记录、恢复身份、资源、队列状态、业务阶段、已开始次数、下次执行、失败原因、游标及逐点进度。分页继续采用服务的 next，即使当前可见页为空也能向后翻页；页面只显示 `allowed_actions` 允许的操作。

任务取消和重试保存确认时的完整不透明版本字符串。任务状态更新后，顶部操作说明与详情中的业务阶段同步显示，服务返回的409原因继续保留。409 会刷新详情和列表，用户核对新的状态之后再次操作；403 清除无权读取的详情和操作，401 触发共享会话失效流程。运行中的取消请求显示停止处理中；补算在 `phase=rollups`、`progress=1` 时继续显示汇总阶段，完成以队列 completed 和补算 completed 阶段共同判断。取消补算的确认文案为“停止后续补算，保留已提交的历史结果和修订”。

浏览器任务验证采用隔离 PG18 Schema、最多三个应用连接和串行管理连接，生产 HTTP 服务及 River worker 执行任务。无效时间范围产生失败与耗尽重试；修复夹具保存 failed 业务状态和原身份，避免 pending 写入产生新的任务代际。临时的汇总写入错误触发器用于验证逐点完成后的失败恢复，之后删除。重试前后对照 task、business、River、replay、固定范围与游标，并核对正式状态和数据库结果。角色只读、资源不足及登录后停用身份分别保留证据，报告省略凭据。

验收修正后的14项前端回归、TypeScript 6兼容检查、TypeScript 7正式检查和生产构建均通过。原有干净安装目录只更新任务页面、状态说明与对应测试，重新构建的五项产物与当前构建字节一致；首次安装与旧产物的证据保留在 `initial-delivery` 和原始日志中。

运行应用的current构建快照记录于2026-10-05 01:04:01 +08:00，后续夹具模板的文件修改时间为01:09:35，fixture-control二进制文件时间为01:09:38。后续控制命令准备新的业务身份与一条原始观测，并以failed状态修复范围；运行应用继续使用current二进制处理任务。构建时两份夹具的完整源码副本未独立保存，构建摘要、后续时间和浏览器流程关系保存在 `review-followup/fixture-provenance.json`；临时Go包装器已在第一次清理时删除。

现有 ECharts 5.6.0 的 npm 审计包含一个 moderate 项，公告 [GHSA-fgmj-fm8m-jvvx](https://github.com/advisories/GHSA-fgmj-fm8m-jvvx)列出的影响范围为 `<6.1.0`，修复版本为 6.1.0；机器可读输出保存在 `npm-audit.json`。该依赖纳入第四阶段 E4-10 升级及再次审计。当前生产构建仍有大于 500 kB 的主模块提示，后续页面迁移可处理拆分；TanStack 全面迁移、ECharts 业务页面升级和参数表单按照第四阶段任务继续推进。
