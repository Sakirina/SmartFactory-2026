# 定义应用用例、API 契约与编号迁移

本记录对应第一阶段的定义草稿保存、校验和发布流程，实施日期为 2026-10-04。可执行验证记录位于 `.local/evolution/architecture-contracts-verified.json`，其中保存每条命令、工作目录、退出码、测试名称、输出文件和实际工具路径。本文件依照现有仓库设置保存在本地 `docs/evolution`，发布构建继续使用项目既有的文档选择规则。

`Platform/internal/application/definitions.go` 提供 `Definitions.SaveDraft`、`ValidateDraft`、`PublishDraft` 和 `LoadDraft`，调用方传入 `identity.Principal` 与明确的请求结构。应用用例处理用户权限、定义所属资源、选择器设备与资产、策略条件、正常动作与降级动作，以及全部递归依赖的读取权限；`LoadDraft` 同时供差异查看和模拟调用。HTTP 中间件负责会话认证，今后的其他调用入口可以在完成认证后使用这些用例。

应用用例在执行权限判断前读取用户、授权记录集合和资源祖先关系，保存读取时的文档版本；提交时，它在同一写入事务中检查这些版本和授权集合成员，同时检查草稿、当前已发布定义及全部递归依赖的版本。资源迁移、用户权限更新、授权新增或删除、依赖停用，以及草稿并发编辑，都会使使用旧读取结果的写入返回版本冲突。该实现目前读取完整授权集合；后续授权索引优化需要继续保留新增、删除授权时的并发检查。

定义流程按照以下顺序执行：

1. 保存草稿时，用例检查调用者对旧草稿、当前定义和新定义引用资源的权限，按照 `expected_version` 更新草稿，并写入 `definition.draft.save` 审计。尚未完成的节点图可以保存，图结构完整性由校验和发布处理。
2. 校验草稿时，用例读取指定草稿版本，调用既有规则校验器，并在事务中确认读取版本仍然有效后写入 `definition.draft.validate` 审计。审计内容包含草稿版本和完整校验结果，校验未通过的结果也会留存。
3. 发布草稿时，用例检查发布权限、云端发布条件、草稿版本与基础定义版本，然后调用共享的 `definitioncommit.Commit`。该提交路径在同一事务内写入定义、输出目录、草稿新版本、ThingsBoard 与边端同步 outbox，以及发布审计；初始化时使用的 `engine.Service.Publish` 也调用这个提交路径。事务中的任一步骤失败后，整次发布回滚。

`Platform/internal/api/definition_routes.go` 使用 Huma v2.39.1 的 `humago` 适配器注册三个草稿操作和 `GET /compatibility`。请求体、路径参数、响应和错误均有 Go 类型；JSON 解码保留 `json.Number`，拒绝未知字段和连续的多个 JSON 值，正文上限为 2 MiB。校验和发布接受可选的 `expected_version`，已有调用方继续可以发送空正文；当前前端发送明确版本。错误正文继续使用 `{ "error": ... }`，补充的字段校验信息放在 `details`。

`DefinitionOpenAPI` 执行与运行中服务相同的注册代码，`sf-contracts` 将这四个操作及其 schema 合并到 OpenAPI 3.1.0。当前导出包含 49 个操作和 51 个 schema，其中四个操作标记 `x-contract-source: typed-registration`，其余 45 个操作继续标记为 `legacy-source`，由原有 AST 导出方式提供契约。`GET /contracts/operations` 返回当前类型化操作的运行时契约。兼容状态包括共享兼容表、数据库驱动和实际应用的迁移记录；`contracts/compatibility.json` 使用与服务相同的兼容表。

`Platform/pkg/contracts/typescript.go` 根据导出的 OpenAPI 生成 `contracts/1.0/api-types.ts`，其中包含模型类型、操作输入输出与 `operationRoutes`；未限定结构的 JSON 值使用 `unknown`。`Frontends/src/generated-client.ts` 从该文件取得草稿操作的路径与类型，并通过现有 `api.ts` 发送请求，继续使用会话认证、`parseAPIJSON` 与 `RequestError.status`。定义编辑器在保存后使用返回的草稿版本进行校验，发布确认保存被审核的草稿 ID 和版本，避免后续操作意外选择另一个版本。

`Platform/internal/store/migrations.go` 使用 Goose v3.28.0 加载内嵌迁移，编号 1 为 SQLite 或 PostgreSQL 的现有结构基线，编号 2 为 inbox 过期字段，编号 3 为同步历史初始化。迁移复用旧表和索引；同步初始化保留已有 sequence，仅为缺少的文档版本追加记录。`goose_db_version` 保存执行状态，`sf_schema_migrations` 保存编号、源文件名、SHA-256 和执行时间；SQL 文件与 Go 迁移源文件均嵌入二进制，校验值与迁移在同一事务中写入。启动时发现校验值缺失、内容变化或数据库版本高于当前程序，会返回明确错误。升级采用向前迁移，自动降级入口会返回错误；PostgreSQL 迁移使用 Goose 会话锁，SQLite 进程内启动使用迁移互斥锁。

本阶段与观测实现协作，在应用用例中建立 `definitions.draft.save`、`definitions.draft.validate` 和 `definitions.draft.publish` span，并携带调用者、请求、草稿、定义和草稿版本标识。运行中 Application 的测试启动 HTTP 服务与测试 OTLP collector，验证传入 W3C trace、HTTP 请求 span、应用用例 span 和数据库事务 span 的父子关系，以及响应中的请求 ID。

验证使用 `/opt/homebrew/bin/go` 的 Go 1.27.1，以及工作区 `.local/toolchains/node-v24.21.0-darwin-arm64/bin` 的 Node 24.21.0、npm 11.19.0。普通 Go 验证覆盖 `internal/application/...`、`internal/api`、`internal/store`、`internal/engine`、`pkg/contracts`、`cmd/sf-contracts` 与 `internal/app`，81 项测试（含子测试）通过；需要独立 PostgreSQL 环境的测试在该轮次按条件跳过。新增 PostgreSQL 迁移测试随后在独立 PG 18 环境中通过，它连续启动两次，核对文档和版本历史、大整数 JSON 原始内容、已有同步序号与缺失版本补写、待发 outbox、审计链字段、现有观测分区和 inbox 过期字段。SQLite 测试另外验证迁移失败时 DDL、版本记录和校验值共同回滚，以及修复未应用迁移后的再次执行。

生成契约通过 Draft 2020-12 schema 检查、24 个可编辑示例检查和所有 OpenAPI 引用检查；连续两次生成的文件 SHA-256 完全一致。Node 24 下的前端 `typecheck` 和生产构建均以退出码 0 完成，`git diff --check` 通过。Vite 仍提示主脚本超过 500 kB 的构建提醒。浏览器中的完整交互验收由主智能体使用重新编译的 Pilot 完成，其结果独立记录。
