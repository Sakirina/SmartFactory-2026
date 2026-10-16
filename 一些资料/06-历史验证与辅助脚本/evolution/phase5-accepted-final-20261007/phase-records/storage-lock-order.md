# PostgreSQL 事务、日志与任务锁顺序

写入事务采用 ReadCommitted，业务回调期间按对象取得锁；SQLite 使用单连接与 Serializable。分区缓存只在成功提交后更新，回滚中的建表与退休操作不会改变缓存。当前实现与测试证据汇总见 [存储与任务实施记录](storage-tasks.md)。

事务按以下流程执行：

1. 授权及依赖读取使用 `Tx.Read` 的共享对象锁；`Tx.ReadCollection` 先取得共享成员锁，再按身份排序读取成员并持有共享对象锁。两个事务读取相同人员、授权或资产关系时可以并行，成员新增／删除取得排他成员锁，现有成员修改取得对应对象排他锁。需要替换整个集合的调用在读取前按 kind 排序使用 `Tx.LockCollection`。会话版本参与授权检查，登出通过 `Tx.Delete` 删除。
2. 业务读改写使用 `Tx.Get`、`Put`、`ImportDocument` 与 `SetEphemeral` 的对象排他锁，不存在的身份也拥有确定锁键。采集按序列键排序取得锁，再判断晚到数据；inbox 身份、文档 CAS、观测写入、outbox 和 River 入队均在此阶段完成。复杂多对象写入可能形成数据库死锁，PostgreSQL 40001／40P01 返回同时满足 `ErrRetryable` 和 `ErrConflict` 的错误，HTTP 返回 409，Store 每次只调用业务回调一次。
3. River `InsertTx` 与批量 `InsertManyTx` 使用 Store 的同一个 `*sql.Tx`。业务映射与正式 River 行同时提交，回滚会同时撤销两者。River 取得任务后先提交获取任务的事务，再运行 worker；任务执行与外部网络请求期间释放获取任务时使用的数据库连接。取消／重试先检查授权版本，再取得业务与任务锁。
4. 同步变更和审计内容在业务阶段复制到 Tx，业务回调成功后 finalize 取得提交日志 advisory 事务锁，再读取当前可见的 `MAX(sequence)` 与各源 audit head，分配序号、签名并追加日志，随后立即 Commit。finalize 只处理日志；分区、文档和任务操作在此之前完成。
5. 提交日志锁持续到 Commit。后一个事务取得该锁后，通过 ReadCommitted 新语句读取刚提交的序号和链头；较早开始但较迟提交的事务会获得较后的序号。业务、inbox、outbox、River、同步及审计在 finalize 或 Commit 失败时共同回滚。

分区查询及写入使用分区共享锁，创建和退休使用排他锁。退休事务取得锁后重新查询实际表是否存在、是否为空；同时退休同一个分区的两个调用可以依次完成，后续采集按需重新建表。锁升级被 PostgreSQL 判定为死锁时返回可识别的重试冲突，调用方可重新发起整项业务。

归档候选读取、编码、压缩与完整解码校验在写入事务之外完成；每块最多 4096 点及 8 MiB 原文。最终写入事务核对旧归档块身份、摘要和内容，再对原候选行加锁并比较主键、观测时间与原始 JSON；删除仅使用确认过的候选身份。编码期间新增的晚到数据继续保留在热表，候选或原块变化返回冲突。归档保留期的部分块改写也在事务外准备，并按原块 CAS 提交。

编号迁移 1 至 3 保持原字节与 checksum。启动锁依次保护 Goose 迁移至 3、River 官方迁移至 8、Goose 迁移 4；River 每项迁移遵循官方独立事务，migration 4 记录任务业务映射及其校验值。River 已迁移而 migration 4 失败时，下一次启动验证 River 状态后继续 migration 4，原有三个校验值保持一致。

真实 PostgreSQL 18 测试覆盖两个独立对象同时进入回调、18 次同对象递增且回调各执行一次、不存在身份的创建竞争、迟提交游标、每源审计链、finalize 失败共同回滚、分区创建及退休竞争，以及实际 HTTP 死锁的 200／409 响应。两个经过身份授权的 `SaveDraft` 调用通过数据库 UPDATE 触发器屏障证明同时到达不同对象写入，共享人员、授权与资产读取仍可并行。

领域用例通过 `application/repositories.go` 的 `DocumentQueries`、`DefinitionRepository` 与 `TaskRepository` 使用版本化查询、事务提交和任务操作，`Definitions.Store`、`Tasks.Store` 已声明为相应接口。发布提交使用 `definitioncommit.Repository`；授权版本 SQL 由 `store.Tx.CheckRevisions` 执行。worker 的 `tasks.Repository` 提供归档调度、补算失败保存和投影完成等业务操作，`tasks.Backend` 仅在构造时增加正式 River 客户端工厂；运行中的 Service 保存 Repository，SQL、驱动选择与投影完成事务在 `store/task_runtime.go`。控制与其他历史分析路径在后续对应阶段按此约定继续迁移。
