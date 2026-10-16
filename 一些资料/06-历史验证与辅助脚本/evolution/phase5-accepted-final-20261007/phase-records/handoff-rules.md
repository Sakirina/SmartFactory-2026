# 规则计算改造交接记录

交接时间为 2026-10-04。本轮架构与契约工程师已完成第一阶段工作，并根据主协调者此前的任务分配开始第二阶段规则计算；项目维护者调整了各阶段的工程师职责后，本轮将第二阶段的部分实现保存至此检查点，后续由专职规则工程师继续。第三阶段尚未开始，本轮不再扩展规则功能。

第一阶段记录位于 `docs/evolution/architecture-contracts.md`，其实际验证快照是 `.local/evolution/architecture-contracts-verified.json`。该快照产生于本轮规则改造之前；当前工作目录包含以下尚未完成的第二阶段代码，当前测试结果应以 `.local/evolution/rules-handoff-verified.json` 为准。主协调者已经完成浏览器中 `climate-average` v1 至 v2 的保存、校验、差异查看和发布，浏览器、数据库与 OTLP 的验收证据由主协调者维护。

## 当前编译与测试

Go 1.27.1 下，针对 `internal/rulecore`、`internal/compiledplan`、`internal/engine`、`internal/application/...`、`internal/api`、`internal/app`、`internal/cloudsync` 和 `pkg/precise` 执行 `go test -run '^$'`，退出码为 0。相同范围的 `go test -count=1 -json` 记录 54 项通过、8 项失败，退出码为 1；`git diff --check` 通过。每条命令、输出、测试名称及 20 个相关源文件的 SHA-256 均已写入 `rules-handoff-verified.json`。

六个引擎测试和一个场景测试直接使用 `Store.Put` 写入定义，随后调用运行层，尚未执行新增的计划准备入口，因此返回 `execution plan has not been prepared`。这些测试是 `TestQueuedRealtimeObservationCreatesRecomputationBeforeAcknowledgement`、`TestTypedGraphAndDerivedRevision`、`TestCounterSurvivesDuplicateAndRestart`、`TestReplayUsesMoreThanOneDayAndRestoresLiveCounter`、`TestHistoricalAlarmKeepsAcknowledgementAndCurrentEpisode`、`TestStrategyContinuesWhileHistoricalMergeOwnsCalculationLock` 和 `TestSceneTimersQualityHysteresisAndRecovery`。后续应将夹具经过正式发布或明确的旧定义准备过程，同时验证重启后可以载入持久计划。

`TestPublicationWaitsForCommittedNodeCursor` 使用了缺少名称、schema_version、group_id 和节点的不完整定义，新增云边导入准备会拒绝该定义，错误为 `definition compilation`。该测试需要用完整可执行定义验证游标提交行为，另外为旧无效定义制定并验证明确的导入处理方式。`internal/application` 与 `internal/api` 当前回归通过；新建的纯计算核与计划保存包尚未增加专门测试，其确定性、摘要校验和运行复用仍需专职工程师验证。

## 文件与接口

`Platform/pkg/model/execution_plan.go` 新增 `ExecutionPlan`、`ExecutionNode`、`NodeParameters`、`ExpressionTree` 和 `AssetVersion`。`model.Definition` 在 `Platform/pkg/model/model.go` 中增加可选的 `execution_plan`，用于让正式定义及其版本历史、云边同步携带同一份计划。计划包含格式、定义 ID、定义版本、定义内容摘要、计划摘要、排序后的节点、输入连线、类型化参数与表达式指令树。

`Platform/pkg/model/node_types.go` 将 `NodeKinds` 改为从 `NodeCatalog` 派生，目录包含 12 种现有节点的名称、适用定义、端口、参数类型、默认值、枚举、范围、单位、说明和是否需要状态。空连线端口被解释为默认的 `value` 端口。`NodeCatalog` 已供编译器使用，尚未提供目录 API，也尚未将参数规则加入生成 schema 和前端表单。

`Platform/pkg/precise/numbers.go` 保存从 `store/retention.go` 原样提取的 `Number`、`AggregatePoints`、`MergeAggregate` 和相关类型、辅助函数。`Platform/internal/store/retention.go` 保留公开类型别名和包装函数，其保留期、归档、数据库查询和汇总写入代码继续使用原有实现。提取后的存储回归曾单独通过，第二阶段没有为这次提取添加新依赖。

`Platform/internal/rulecore/expression.go` 将表达式解析与执行分开：`CompileExpression` 只在准备阶段使用 Go parser，输出可序列化指令；`EvaluateExpression` 解释指令树。表达式仍受 4096 字节、256 次操作、32 层深度、最多 16 个函数参数和 4096 位数值预算限制，整数结果使用 `json.Number`。`Compare` 直接执行比较，阈值和回差节点无需拼接或重新解析表达式。`expression_types.go` 为规则表达式补充有限类型检查。

`Platform/internal/rulecore/compile.go` 提供 `Compile`、`ContentHash`、`PlanID` 和 `Verify`，实现节点与参数验证、端口验证、拓扑排序、策略条件检查和计划摘要。`Compile` 使用带 `UseNumber` 的 JSON 复制，避免计划与草稿共享可变 map；表达式变量限定为运行核提供的变量。编译器只处理定义本身，依赖定义的可用性由运行准备与应用用例处理。

`Platform/internal/rulecore/execute.go` 提供 `Execute(plan, Frame)`。`Frame` 明确传入当前观测、窗口数据、其他输入字段、事件逻辑时刻 `ClockMS`、新鲜度参照时刻 `FreshnessAtMS`、新鲜度阈值、初始状态、计算实体和资产版本；计算核没有数据库读写和系统时钟读取。返回的 `Evaluation` 追加计划身份、窗口输入 ID、资产版本及逐节点输入、输出、状态和错误记录。引擎原有 `RuntimeState` 与 `Evaluation` 已改为这里的类型别名。新核对混合单位聚合、非法计数增量和 int64 计数溢出返回错误，这些行为还需要专门样本验证。

`Platform/internal/compiledplan/store.go` 提供 `Compile`、`Put`、`Import` 和 `Load`。`Put` 在既有事务内保存 `compiled_plan` 文档；同一 ID 已存在时检查定义摘要与计划摘要，拒绝不同内容覆盖。`Load` 读取并验证已保存计划。`Import` 对旧定义编译，对带计划的新定义验证并保存；目前尚未在该入口检查递归依赖的可用性。

`Platform/internal/application/definitioncommit/publish.go` 在分配正式定义版本、状态和生效时间后编译计划，将计划放入定义，在同一事务内保存 `compiled_plan` 和原有发布对象。`Platform/internal/application/definitions.go` 保存草稿时清除旧的 `ExecutionPlan`，正式发布会重新编译。当前发布校验和最终计划生成分别执行一次编译，均位于准备过程；后续可在保持最终版本身份准确的前提下减少这两次编译的重复工作。

`Platform/internal/engine/definitions.go` 的 `Validate` 已调用纯计算核编译，并另外递归检查依赖；停用定义时清除旧执行计划字段。`expression.go` 保留一次性 `Expression` 适配器，供控制条件与诊断使用。`runtime.go` 和 `timers.go` 的正式运行路径使用 `loadPlan` 与 `evaluatePrepared`，不再逐条观测调用 `Validate` 或解析表达式；公开 `Evaluate` 目前作为一次性诊断入口，仍会准备传入定义。`frame.go` 将数据库读取、历史资产快照、窗口排序、输入字段查询和时刻选择组织为显式 `Frame`。

`Platform/internal/engine/plans.go` 提供 `PrepareDefinition`、`PreparePublishedPlans`、计划缓存及旧规则隔离记录。启动准备遍历已发布历史版本，准备本地计划；旧规则无法编译或缺少依赖时保存 `plan_issue`，运行层识别 `ErrPlanIsolated` 后跳过该定义。当前还需区分数据库操作失败与规则语义错误：`PrepareDefinition` 返回的错误会统一进入隔离处理，而已存在 `compiled_plan` 的校验失败会直接使启动失败。`plan_issue` 尚无查询 API，隔离规则也未形成验收测试。

`Platform/internal/app/app.go` 的修改只有在 `cfg.ApplyPolicy` 后调用 `PreparePublishedPlans`，以及依主协调者要求给 `ai.Chat.Default` 增加 `SF_MODEL_PROVIDER`、`SF_MODEL_API` 环境变量。`Platform/internal/cloudsync/exchange.go` 在导入已发布定义前调用 `compiledplan.Import`，原有兼容字段与同步逻辑保留。两个文件还包含其他工程师的修改，后续编辑应围绕对应调用，不覆盖整份文件。

## 尚未完成的运行路径与契约

连续模拟尚未实施，现有 `Simulate` 和 `POST /drafts/{id}/simulate` 仍然只接受一个 `point`。后续需要在入口编译一次，然后按照明确顺序处理 `points`，将此前输入合并到后续窗口并延续状态，提供初始状态、时刻、输入摘要和逐点结果；模拟应继续保持数据只读。`Evaluate` 的单次诊断行为已经使用新核，连续输入、乱序、同时间戳顺序、坏质量、缺失输入及窗口精度都尚待验证。

实时处理、定时器和补算目前都通过 `processLocked` 或 `evaluatePrepared` 使用同一计算核，但旧测试夹具与部分直接写定义的入口尚未经过准备。编译计划的数据库读写没有新增 SQL 表，仍使用现有 `documents`；启动准备需要验证所有历史版本、当前有效版本、无效历史版本、缺失依赖与重启恢复。云边批次还需要处理同一批次内依赖顺序、无效旧定义以及已携带计划的摘要/格式拒绝。

`PreparePublishedPlans` 当前会缓存校验通过的既有计划，并为无法准备的旧规则保存诊断，后续需要确认依赖检查与隔离记录在规则恢复后的处理。计划核自身的 `Execute` 只检查计划格式，完整摘要校验在保存和加载适配层完成；新增外部入口应继续通过该准备与验证流程。当前 `evaluatePrepared` 将策略中的超时应用于整次图计算，旧实现曾为每个表达式分别设定超时，后续工程师需要明确并测试这一执行预算的语义。

生成文件仍停留在第一阶段的 51 个 schema、49 个 operation 快照，尚未导出 `execution_plan`、新节点元数据和新求值结果，`contracts/1.0/api-types.ts` 也尚未重新生成。后续应在模拟、元数据和计划诊断 API 确定后运行 `go run ./cmd/sf-contracts`，使用 `/private/tmp/smartfactory-evolution-python/bin/python scripts/check-contracts.py` 检查引用与示例，再由前端工程师完成类型和页面整合。第一阶段已经使用生成客户端的三个草稿操作继续保留；第二阶段没有继续修改前端页面。

第三阶段的持久控制状态机、动作结果核对、人工干预、隔离历史回放、规则版本比较、实时影子计算及对应 API 均尚未开始，现有 `engine/history.go` 的补算流程仍使用原有任务和结果提交逻辑。

## 计划不变量与协作约定

当前计划格式为 `smartfactory.rules/1`。定义内容摘要计算前清除 `execution_plan`，其他定义字段均参与稳定 JSON 序列化；计划 ID 由定义 ID、定义版本和内容摘要组成。计划 SHA-256 计算前清除计划自身的 `sha256` 字段，覆盖其余全部计划内容。整数通过原有精确数值实现和 `json.Number` 保留，表达式与平均值的小数行为沿用既有实现；下一轮应核对大整数、分数、超出数值预算和序列化往返。

同一计划可以被多次计算，每次计算的状态由 `Frame.InitialState` 提供，计算结果返回独立状态 map。实时、模拟、回放和影子计算应分别管理状态标识，后续不得让候选计算写入正式 `engine_state`、告警或设备动作。持久计划的不可变性依靠创建时版本 0 写入及已存在时的摘要比较，后续需增加并发相同计划、冲突内容和损坏计划测试。

存储工程师 Sol 负责 `store.go`、`sync.go`、审计与归档事务、后台任务和 River 集成，并已经取得 `Platform/go.mod`、`go.sum` 的依赖修改权；迁移编号 4 由他协调，第一阶段编号 1 至 3 的已应用迁移源应保持不变。规则改造使用现有文档保存计划，暂时无需数据库迁移。Sol 已将 PostgreSQL ReadCommitted 对象锁与 `Tx.LockCollection(kind)` 集合锁保存到检查点，`definitioncommit.CheckRevisions` 已先按排序后的 kind 取得集合锁，再查询对象版本及授权集合成员。成员检查仍使用事务 SQL，受到该集合锁保护；这部分需要随存储工程师的交接一并检查。`Recompute(ctx, model.Job)` 签名保持原样，River worker 可据此继续集成。Sol 同样已按职责调整停止扩展，其后续存储工作由主协调者重新分配。

本轮结束后释放上述规则文件的编辑占用。专职规则工程师继续的顺序如下：

1. 阅读当前代码和 `rules-handoff-verified.json`，完成启动、发布、导入及测试夹具的计划准备流程，先恢复八项现有测试，并增加计划身份、损坏摘要、历史隔离和依赖场景。
2. 验证表达式编译与类型错误定位、计算核的精确数值、状态独立性、显式时刻与资产版本，补充同输入条件下实时和模拟结果相同的测试，并检查正式运行路径中没有表达式重复解析。
3. 实施连续模拟、元数据与计划诊断 API，补足应用权限、版本和审计要求，与主协调者及前端工程师确认请求响应后生成契约和 TypeScript。
4. 与存储工程师对齐对象/集合锁和 River 的补算调用，再按照主协调者分配推进第三阶段后端；为每一批功能保存可复核命令、运行结果和当前未完成事项。
