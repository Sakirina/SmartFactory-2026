# 存储与 River 检查点交接

本页保留 2026-10-04 的原始交接状态。后续存储与任务实现、实际 PostgreSQL 验证、恢复语义和当前验收证据见 [存储与任务实施记录](storage-tasks.md)；以下“待实施”“未执行”描述属于移交当时的检查点。

2026-10-04 按最新职责调整，本智能体停止第二阶段存储与任务功能开发，继续完成第一阶段工具链、观测、ThingsBoard 和 Envoy 验证。以下内容是已保存的检查点，接任存储工程师需要先审查并完成真实 PostgreSQL 并发验证，再确认 E2-02 验收状态。

已有改动覆盖 `Platform/internal/store/store.go`、新增 `transaction.go`、`sync.go`、`audit.go`、`audit_sync.go`、`data.go`、`archive.go`、`retention.go`。`store.go` 的业务写入移除全程进程互斥与事务起点的全局 advisory 锁，PostgreSQL 使用 ReadCommitted，SQLite 继续单连接与 Serializable。`Store.mu` 现在仅用于成功提交后的分区缓存更新。Tx 增加 managed、locks、changes、auditFinalizers、retiredPartitions，全部字段允许为空，并按使用情况初始化；直接构造 Tx 的编号迁移保持旧行为。

`transaction.go` 提供 `ErrRetryable`（同时满足 errors.Is(err, ErrConflict)）、`Tx.LockCollection(kind)`、`Tx.List(kind)`、`Tx.Delete(kind,id)` 与 `Tx.DeleteCollection(kind)`。事务普通文档 Get/Put/ImportDocument/SetEphemeral 取得共享集合锁和排他对象锁，锁键按确定 SHA-256 计算，不存在对象也参与竞争。需要完整集合成员检查的事务在读取对象前按 kind 排序取得排他集合锁。集合共享锁升级会返回明确重试冲突，PostgreSQL 40001/40P01 统一归类为 ErrRetryable；Store 不自动重复业务回调。

同步变更的 JSON 字节在 RecordChange 调用时复制进 Tx。当地审计 Snapshot 在 Audit 调用时先序列化为 RawMessage，避免后续修改影响审计；外来审计在业务阶段完成来源、原始 JSON 哈希及签名验证并复制内容，在 finalize 中重新读取当前源链头、核对重复/缺口并追加。finalize 仅在有日志时取得 advisory 872190006，读取最新可见 MAX(sequence)，追加同步和审计，然后立即 Commit。业务对象、分区及 River 入队必须在 finalize 之前完成，finalize 不取得新的业务对象锁。

分区 EnsureDay 在事务中取得分区共享锁并查询当前 schema 的实际 pg_class；不存在分区时升级为排他锁并创建。分区锁升级可以产生数据库死锁检测，此时按 ErrRetryable 返回。创建或删除缓存只在 Commit 成功后更新；archive 的空分区删除与 retention 删除已纳入排他分区锁。接任者需要继续检查多个 Store 进程同时退休同一分区时，列表读取后另一事务先删除导致的不存在表情况，以及完整的创建/删除竞争测试。

定义发布的 `internal/application/definitioncommit/publish.go` 已在 CheckRevisions 最前面为 Members 非空的集合排序并 LockCollection，然后执行原有集合版本和逐对象版本核对。`internal/identity/identity.go` 的 ApplyBundle 在事务起点 LockCollection("grant")、LockCollection("user")，替换授权使用 DeleteCollection；`internal/configcenter/subscription.go` 的参数恢复删除调用 Tx.Delete。retention 对过期文档删除前新增 Get，并比较扫描时版本和原始内容，变化后留待下一轮。普通 session 登出仍是原有 DB.ExecContext 删除，该路径尚未纳入 Tx 对象锁；请审查权限/会话并发要求后决定相应接口。Astra 对 retention.go 的改动是把纯数值计算移入 pkg/precise，保留 alias/包装函数，存储交接需保留这一部分。

归档计算外移与精确集合 CAS 尚未实施，现有 ArchiveObservations 仍在事务内部读取、压缩、验证并使用前缀删除。当前仅分区缓存与锁的局部准备已经修改；不能把 E2-03 记为完成。接任者应按 `storage-lock-order.md` 的设计，在事务外完成候选读取和编码，短事务核对原始主键集合与内容、旧归档块摘要，再精确删除确认的主键，并测试期间新增的晚到记录保留。

River 正式 v0.48.0（2026-10-01，commit c2c380490d14df8efc28b86fbe7be1cf93930405）以及 riverdriver、riverdatabasesql、riversqlite、riverpgxv5、rivershared、rivertype 同版本已写入 Platform/go.mod/go.sum 并取得共享缓存；pgx 升至 v5.11.0。官方 riversqlite 源码要求单连接以避免 SQLITE_BUSY，当前 Store 已符合这个前提。尚未运行 River 同事务或 worker 兼容试验，没有新增 River 迁移，没有实际替换原有归档、补算或投影轮询。下一位工程师必须先证明 InsertTx 使用 Store 的同一个 *sql.Tx、回滚后无任务、提交前 worker 看不到任务、重复任务/重试/重启行为，再完成实际 worker 替换和预算；不能改成简化自研队列来取得测试通过。

原有 Goose 编号迁移 1–3 的 SQL 与 Go 源文件保持字节和 checksum；migration00003.go 直接构造 Tx，仍直接 SQL 补齐同步历史。migrationProvider、MigrationManifest 与 compatibility 数据库范围目前仍为 1–3，新迁移编号 4 已由 Astra 预留给存储工程师。官方 River 的 Migrator.MigrateTx 已标记 deprecated，因为某些迁移不能成批置于同一外层事务；接任者需要选择与 Goose checksum 及 River 内建迁移协调的正式路径，并同步数据库最小/最大版本。不要改写已应用的 migration00003.go。

共享环境为 GOPATH=/private/tmp/smartfactory-evolution-gopath、GOMODCACHE=/private/tmp/smartfactory-evolution-gomodcache、GOCACHE=/private/tmp/smartfactory-evolution-go-cache、GOTOOLCHAIN=local，验证可以设置 GOPROXY=off；必要依赖与镜像传输已获得用户授权，保留 GOSUMDB 验证。MCP 官方 SDK v1.8.0 及传递依赖已由本智能体统一写入，root 正在实现 AI/MCP 适配；后续 go.mod/go.sum 修改须由接任工程师与 root 协调。未执行清理未使用依赖的 tidy，以保留交接时已经准备的正式依赖。

检查点这一轮的编译测试结果为：store/application/identity/configcenter/api 全部通过；`TestPostgresNumberedMigrationsPreserveExistingSchema` 在 PG18 通过，输出 `.local/evolution/storage-checkpoint-postgres.log`；HTTP 草稿 OTLP 测试通过。该轮完整 app 存在规则检查点失败：`TestSceneTimersQualityHysteresisAndRecovery` 的 scenes_test.go:83 返回 `execution plan has not been prepared: light-off version 1: record not found`，已通知 root/Astra，规则工程师将在其职责内处理。当前未新增或执行不同对象同时停留、同对象递增、不存在对象争用、迟提交游标、死锁 HTTP409、日志 finalize 失败共同回滚等 PG 真实并发专项；这些是 E2-02 必须补齐的验收内容。

临时 PG18 容器 `smartfactory-evolution-pg18-20261004` 继续运行，2 CPU、1 GiB 内存、512 MiB tmpfs、max_connections=30，凭证位于权限 0600 的 `.local/evolution/pg18-test.json`。root 的 pilot 应用和收集器已结束，sf_pilot_20261004 schema 保留；CE/Edge 的临时数据库分别为 sf_evolution_tbce_20261004、sf_evolution_tbedge_20261004，数据库继续保存在当前 PG 容器；CE/Edge/Kafka 原生验证服务会在有限验证结束后停止。任何重建或停止 PG 容器需要先通知 root。新测试按唯一 schema 隔离并清理自己的 schema；工作卷保留至少 1 GiB，构建和临时状态使用 /private/tmp。

第一阶段验证服务已经于 15:10:53 UTC 结束，PG18 与全部临时数据库继续保留。最新资源证据 `.local/evolution/post-cleanup-resources.json`：PG18 为 2 CPU/1 GiB、max_connections=30/shared_buffers=64MB；清理后内存331.7 MiB，SQL探测时仅该探测自身一个连接。凭据仍为0600；`.local/evolution/service-cleanup.json` 保存临时服务归属与实际退出事件。接任工程师可以在root协调下使用唯一schema，并取得独立PG16短时迁移环境。

相同规则检查点的失败输出继续保留在 `.local/evolution/rules-handoff-regression.stdout.txt`；该文件是Astra在移交规则工程师前保存的回归快照，后续规则工程师按其新实现更新验证状态。
