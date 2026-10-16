最终独立验收任务说明

独立验收工程师按 `acceptance.json` 的45个目标核对当前实现、运行结果与最终交付包，重点确认跨阶段修改后旧有结论仍适用于最终源码。主协调者负责需求解释和验收状态，实现负责人负责修复各自负责的缺陷；审查工程师负责定位、复现和独立再验证。

首先读取各阶段报告和主协调者复核，建立需求、实现、受测版本、命令、结果及交付文件的对应关系。早期测试、截图和二进制是各自时点的记录，最终测试与交付源摘要需要单独比对。检查通过、跳过、未执行、本地模拟协议端点、实际数据库、实际浏览器及实际云边进程的验证范围，报告中保留其区别。

审查流程如下：

1. 对最终源码执行两个Go模块适用全量回归、前端类型检查与生产构建、生成契约一致性、样本和发布工具测试；对并发共享状态运行有针对性的race检查，记录每项失败和跳过原因
2. 核对PostgreSQL迁移、领域并发、提交可见游标、审计、归档CAS、River同事务入队与恢复，以及Kafka和NATS持久数据迁移的真实记录；必要时使用已有隔离样本复现关键结果
3. 检查规则全部发布和加载入口、不可变计划与数值语义，验证连续模拟、正式补算、独立回放、版本比较和影子计算各自的状态、结果与设备动作语义
4. 对控制状态机、动作日志、执行权、未知结果核对、人工恢复及三节点短时协调进行复核，实际检查故障前后发送次数、身份、授权、对象版本和审计
5. 在浏览器完成主要业务链：草稿发布、任务与连续模拟、控制核对和历史分析、告警与工单交接、模板及AI依据、节点配置与分批发布；核对查询分页、增量恢复、撤权、精确整数和三个页面入口
6. 从最终交付包运行验证脚本与隔离启动入口，核对包内摘要、依赖和许可、公开资料、部署停止及保留数据再次启动，确认当前程序、规则、配置与发布状态一致
7. 形成 `.local/evolution/final-independent-review.json`，为45项分别列明结论与可定位证据；必要修复完成后重新验证受影响范围，再向主协调者提交整体完成建议

审查覆盖本轮已经约定的有限功能、恢复和并发检查，完整UOS安装恢复、连续断云24小时、受限链路专项及实际24小时容量测量按原范围处理。既有用户工作树修改、原持久容器和卷保持原用途；临时数据库与运行服务使用独立名称，报告省略凭据，结束自建进程并记录清理结果。

第三阶段已验收源码归档为`/private/tmp/smartfactory-phase3-accepted-final-20261005/manifest.json`，原路径与595份保存源码、5份正式前端产物的对应关系见`.local/evolution/phase3-preserved-paths.json`。后续阶段会修改当前工作树；核对第三阶段历史报告时使用其受测版本及保存路径，当前组合另外验证并记录实际输入。

第四阶段业务流程专家为设备参数与模板能力核对补充带版本的`connector_configuration`及相关配置应用用例，配置依据包含来源、版本与下发确认状态。开始本任务时读取该专家最终契约和验证报告，核对节点身份校验、配置分发、生效确认及批次协调是否使用已有正式设备与连接器配置记录，并检查新节点状态或凭据引用字段与相应记录的关联。模板实例化读取固定配置版本并生成草稿，配置下发由明确的配置操作触发。

第五阶段独立服务组合按`phase5-coordination.md`中的职责约定核对：cloud保存用户、会话、授权、资源、工作负载身份和发布业务，config保留独立的程序配置与密钥库，通过mTLS在线核验原调用身份。实际组合使用独立cloud、config及至少两个edge进程和各自数据库。检查固定版本读取不改期望、显式发布目标按代际CAS、已有订阅尊重固定目标、撤权与凭据轮换、回调超时、配置中心不可达时的本地缓存运行，以及SQLite跨服务回调的事务调用次序；最终发布回执核对当前身份、目标和实际消费者结果。

主协调的第五阶段验收关注记录保存在 `.local/evolution/phase5-acceptance-watchpoints.json`。最终组合另外核对：代理换用新实例后，相同期望发布能够继续报告，旧实例被拒绝；准备阶段的确定性组件失败具有持久结果并停止后续批次；在新程序就绪、活动状态尚未持久化的时刻中断，恢复后能够确认实际进程或显示可处理事项；持久发布缓存采用节点原有主密钥加密，注入的密钥值在公开记录和缓存中均无明文。普通心跳保持人工动作的业务版本，业务条件变化继续产生版本冲突。

连接器组合使用所属Edge正式保存的固定版本及密钥引用，经公共登记与受认证解析进入发布；核对节点适用范围、动态应用、静态准备及进程启动后的实际消费者值。授权退回旧内容使用明确应用代次，已有延迟任务保护继续有效；普通配置保存与固定发布目标的关系需要从实际采集消费者读取核对，报告中的引用、摘要和版本应与当前采集内容一致。正式启动入口同时保存原有数据库、主密钥、云边同步、NATS及原生平台运行选项。

发布报告时效约定：历史批次保持既有完成结果，代理联系时间与实际运行报告时间分别保存；详情提供 `last_report_ms`、`report_age_ms` 和 `report_fresh`，按15秒判断实际报告时效。批次推进依据有效的实际运行报告，页面同时展示最近报告时间和过期状态。验证保持代理查询期望的同时停止实际子进程或使运行检查失败，确认过期状态与后续批次处理，再核对程序恢复后的报告更新。

实际复现的问题另登记于 `.local/evolution/phase5-coordinator-findings.json`，最终审查逐项核对首次失败及修正后的相同场景。P5-N01涉及发布移除后旧参数密钥读取，需要验证当前完整目标、身份代际和实例代际共同限制密钥解析，空发布目标保留为空、旧运行报告被拒绝，并检查其他工作负载的目标继续可用。

P5-N02的第六轮独立服务验证复现撤销恰好发生在重连间隔时401沿用旧缓存，第七轮和补充PG/race已由专家及主协调核对通过。最终独立复验覆盖缓存启动、流重连、网络或权威服务暂时不可用，以及明确401/403拒绝后实际策略和连接器清理，并以恢复后的新实例、代际和报告时间确认恢复。

P5-A01为主协调在正式发布夹具中复现的验证错误密钥回显：轮换请求缺少expected_version时，422详情包含完整请求体中的credential。由公共API负责人修正并补充验证，最终独立检查同时覆盖父对象缺字段、凭据自身长度或格式错误，确认字段位置与错误说明可用且响应和页面省略凭据值。

第五阶段两个后端专家已完成交付，最终节点过程位于 `/private/tmp/smartfactory-phase5-node-process-final-20261005`，完整发布组合位于 `/private/tmp/smartfactory-phase5-release-process-tenth-20261005`，公共API修正补充位于 `/private/tmp/smartfactory-phase5-release-a01-20261005`。两个专家最终报告分别为 `node-configuration-verified.json` 与 `release-coordination-verified.json`；主协调已经核对实际编译输入、原始执行计数及报告文件摘要。共同245项本地输入中的唯一后续变化是A01公共错误响应修正，关系见 `.local/evolution/phase5-final-backend-combination-root-review.json`。

第四阶段验收归档为 `/private/tmp/smartfactory-phase4-accepted-final-20261005/manifest.json`，原路径与748份源码、15份前端产物的关系见 `.local/evolution/phase4-preserved-paths.json`。第五阶段后端专家报告及工作区中后续可能被交付集成修改的25处引用已经保存，映射见 `.local/evolution/phase5-backend-preserved-paths.json`；节点专家40份源文件另有 `/private/tmp/smartfactory-phase5-node-handoff-20261005/source-manifest.json`。旧报告按对应的固定副本核对，最终组合记录当前实际输入。

P5-N03涉及连接器密钥所属服务暂时不可用时错误分类，以及首次把该服务改由代理管理的启动次序。当前受测流程在原Edge在线时用正式 `--prepare-only` 完成认证、固定内容和工件核验并加密暂存，随后结束原服务、沿用原数据库和主密钥启动代理；再次取得权威信息时还要核对当前分配。正式Linux包及准备、停止、启动入口由构建交付专家继续完成，独立审查将这些入口的实际结果与第十轮进程证据核对。

最终交付的适用外部协议与十万条有限消息专项结果位于 `/private/tmp/smartfactory-delivery-protocols-final-20261005/result.json`，主协调原始证据核对见 `.local/evolution/phase5-delivery-protocols-root-review.json`。原始6276项编译输入保留原采集范围，另有61项实际go-list汇编和头文件通过既有固定副本补充，映射及来源在主协调报告中记录。NATS旧版三节点冷备独立恢复结果为 `/private/tmp/smartfactory-delivery-nats-cold-restore-final-20261005/verification.json`，对应主协调报告 `.local/evolution/phase5-nats-cold-restore-root-review.json`；旧测试夹具修改请求参数的一行已删除，实际覆盖文件、4944份前后相同输入、607包依赖及151份备份文件均已核对，生产执行状态的不可变参数约束继续生效。原等待超时、首次夹具失败及补回备份后命令的条件保留在各自记录。

最终页面验证使用的节点服务入口为 `/private/tmp/smartfactory-phase5-node-frontend-live-20261005/frontend.json`，发布入口为 `/private/tmp/smartfactory-phase5-release-a01-20261005/frontend-fixture.json`，均采用A01修正后的API程序；读取凭据时仅通过描述文件指向的私有文件。若前端专家为后续验证建立新的副本，以其最终报告为准。P5-I12记录尚无发布历史时的首次发布流程核对，独立审查根据前端最终实现和正式包准备入口确认可执行路径。

最终原生组合首次准备时，旧PG18隔离容器的512MiB临时数据盘耗尽，检查点及恢复检查点写入失败后容器自行退出；状态同时记录OOMKilled=true，完整日志见 `.local/evolution/phase5-pg-capacity-incident/summary.json`。原sf_pilot及原生测试库的临时内容已经不可查询，既有历史测试记录继续对应原受测时点。主协调建立同版本、同镜像且使用持久目录的 `smartfactory-evolution-pg18-final-20261005`，私有pg18-test.json已更新。最终PG测试在替代实例中创建新的隔离schema，交付专家按正式安装及引导流程重建本轮CE/Edge测试库，再执行停止、保留数据及再次启动；审查记录应区分重新初始化的库和从备份恢复的库。

P5-F01为首次发布界面缺口：初版只能从已有清单初始化，空发布目录下已有程序工件与正式规则/配置也无法开始。主协调已核对原页面复现、旧源码重构的19份产物及修正后保存四组件清单的实际截图，见 `.local/evolution/phase5-first-release-flow-root-review.json`。该修正首轮完整脚本在保存后的通用抽屉容器隐藏断言失败，具体行为和原始失败分别保留；独立复验以前端最终固定报告和实际空目录完整场景为准，并检查已有清单组合路径。

持久PG测试实例已调整为 `smartfactory-evolution-pg18-final60-20261005`，连接上限60，内存1GiB与2CPU保持原设置。调整之前及之后完整导出四个非模板数据库，cluster SystemIdentifier相同，去除PG18随机restrict/unrestrict控制行后的全部逻辑导出摘要相同；证明见 `/private/tmp/smartfactory-evolution-pg18-final-20261005/pg60-transition/verification.json`。最终PG检查读取当前私有描述，使用新隔离schema。该次准备失败目录为 `/private/tmp/smartfactory-delivery-native-restart-final-20261005`，随后成功记录使用下文的 restart-verified 目录。

P5-F01独立空目录完整用例已经由主协调核对：一项通过、四个组件、正式校验/登记/读取均为200，历史失败保留。P5-F02为刷新回调覆盖后来填写的操作原因，P5-F03为发布只读页面请求配置管理目录返回403后详情消失；前端专家保存首次失败、旧源码和实际产物，最终独立审查按 `.local/evolution/phase5-coordinator-findings.json` 核对修正后的相同条件、部分权限和当前对象切换。

原生停止与保留数据重启最新成功记录已固定为 `/private/tmp/smartfactory-delivery-native-restart-verified-20261005/verification.json`，摘要 `88feecd5b0d8906009132aac2aa9f21cbfbddc702872f0282ef48ce3e7aef132`。主协调报告 `.local/evolution/phase5-native-delivery-root-review.json` 核对3226项实际输入、七份驱动、三十一个命令及三个二进制；全部输入字节另存 `/private/tmp/smartfactory-phase5-native-coordinator-evidence-20261005/retained-inputs.json`。十三项检查通过，Edge停止4.9026秒、PG连接归零，重启后同一时间戳原始/派生值均为22.5、重复消息及两侧审计通过；三个容器最终143且无OOM，两个业务进程退出0。重启后遥测查询20.31384秒返回200，原15秒超时与环境路径准备失败记录保留；正式Linux交付包入口继续由交付专家验证。

最终串行Go/PG结果 `/private/tmp/smartfactory-delivery-regressions-20261005/verification.json` 的摘要为 `1d2aa4c5b824588cd5bd8b7545cbde9c1971930b965eaeb637c3fa3f8c1e0115`；主协调报告 `.local/evolution/phase5-delivery-regressions-root-review.json` 核对Platform 618项与DataTransfer 103项含子测试通过，十二加七项条件跳过，六份编译输入集合与go-list字段完整对应。专用环境和辅助子进程的对应说明读取构建交付最终报告。前端第六轮完整节点流程通过，但snapshot与capture同名JSON覆盖状态原始记录的辅助问题已交回前端修正，最终审查读取其固定组合的新记录。

十九项条件跳过的逐项说明见 `.local/evolution/phase5-delivery-regressions-verified.json`，包含专用环境、辅助入口及本轮恢复父用例的实际结果，71处摘要引用已由主协调核对。补充 `.local/evolution/phase5-historical-source-correspondence-root-review.json` 对49项选择的源码关系进行核对，其中48项与第四阶段最终验收副本相同；较早专家报告的十二处差异中十一处在第四阶段验收时已存在，唯一后续变化为NATS迁移测试移除修改不可变参数的一行，实际冷备恢复已复验。最终独立结论仍需结合各专项实际输入、当前调用关系及本轮普通回归判断覆盖范围。

P5-I13记录五类模板验证环境的协议能力缺项与正式DataTransfer停止观测。原隔离消费者只注册MQTT，后续新增Modbus及OPC UA隔离消费者继续通过生产配置管理、连接器管理和gRPC验证实际固定内容；实际协议通信另有外部专项。Linux网关出现自身worker shutdown五秒超时退出1，尚在按原始日志复现和分类。最终审查核对采集运行专家报告及后续正式Linux组合，不使用未完成的中间轮替代最终入口结论。P5-F03当前权限交叉核对与两类身份页面的部分复验见 `.local/evolution/phase5-permissions-root-review.json`，首次浏览器失败和随后权限请求保留各自时点。

Linux交付检查点为 `.local/evolution/phase5-delivery-linux-preflight-checkpoint.json`，主协调已核对348处报告引用与5997项固定输入、四次七程序构建及八轮原始结果，见 `.local/evolution/phase5-linux-checkpoint-root-review.json`。八轮均保留未完成或失败状态；最新一轮36项已执行检查通过，整体在第二次部署的配置来源409处失败。取消冲突对应业务版本2到3及目标已准备到离线的变化，重新读取版本后取消成功。后续审查读取构建交付最终报告，并核对其完整十四程序包、实际消费者两版本采用、缓存恢复和正式再次启动结果。

P5-R01诊断经主协调核对：默认heartbeat参数当前文档缺失但历史本地修订已到26，普通Put从1开始；删除重建与SetEphemeral之后确认回执也能复现。原三份数据库全部41张应用表和1张SQLite内部表内容相同，2929项编译输入及副本匹配，见 `.local/evolution/phase5-release-restore-diagnosis-root-review.json`。存储修正需与权威固定Parameter.Version分别验证；原备份还保有固定参数版本1，后续审查必须覆盖原缓存启动、初始化与固定发布配置恢复。

P5-D01为生产App受控阻塞订阅中的停止误报，两次旧实现失败和4174项固定输入已核对，见 `.local/evolution/phase5-datatransfer-shutdown-baseline-root-review.json`。修正后的同条件Linux、未结束业务工作保留错误及有限race由采集运行工程师交付；历史完整组合的一次退出继续按其自身证据描述。前端检查点复核见 `.local/evolution/phase5-release-interface-checkpoint-root-review.json`，其中最终源码和静态产物仍未形成统一构建，最终组合待恢复后再验证。

P5-D01最终专项见 `.local/evolution/datatransfer-runtime-followup-verified.json` 与 `.local/evolution/phase5-datatransfer-runtime-root-review.json`：129处引用、34份清单、4483份保留输入已核对；最终race的Go列表包含-race条件，七项顶层十项含子测试通过。相同Linux测试只改变app.go，旧实现退出1、新实现退出0，未结束业务工作仍返回超时；正式采集程序就绪和退出0通过。此前九次缩减环境运行均正常退出，历史完整组合的单次失败继续保有自身记录。独立审查仍核对最终包入口的完整运行和停止。

P5-I14记录原数据故障后的正式修复发布。原故障工件、缓存和数据库继续保留，修正版以新工件和新发布登记；独立审查核对代理能够报告已验证缓存子程序的启动失败并取得新目标，以及固定密钥缓存跨发布复用时的当前身份、节点、程序、用途、凭据代次和在线授权复核。固定Envelope的种类、身份、版本、摘要与凭据引用需一致，明确权限拒绝和变化后的引用继续拒绝；新目标按实际进程及运行报告完成。最终恢复结果读取发布工程师另行提供的recovery报告。

2026-10-05 18:34 补充P5-R01：公共Store修正的主协调报告为 `.local/evolution/phase5-storage-version-root-review.json`，十六个SQLite/实际PG专项场景及对应race通过，原始备份十五份完整，三份副本均核对四十二张表。固定参数版本与本地修订分别核对；最终审查需要继续覆盖默认初始化、原缓存失败后的正式新发布恢复和失败退回流程，当前只确认公共存储部分。

Linux配置来源409补充核查：`.local/evolution/phase5-linux-metadata-root-review.json`核对旧程序、保留数据库副本及十二次实际TLS请求，全部200，二十六份命令日志一致。原完整部署创建失败的瞬时原因尚未确定，最终审查按新的完整Linux组合确认部署创建、实际采用和恢复结果。

P5-R02已确认：`.local/evolution/phase5-release-rule-roundtrip-failure-root-review.json`保存正式返回失败、日志及只读取得的当前发布、规则激活与disabled规则状态。独立审查需要覆盖具有不相交规则的A、B发布往返、同发布再次启动保持版本、当前规则被外部修改时拒绝，以及固定版本和已有审计保留；完整两模块回归的最新报告`.local/evolution/phase5-delivery-current-regressions-root-review.json`发生在该修正之前，后续受影响包与最终实际组合应按固定源码差异对应。

P5-R02最终候选固定输入核对见 `.local/evolution/phase5-release-r02-inputs-root-review.json`，实际源码位于 `/private/tmp/smartfactory-release-restore-recovery-20261005/candidate-r02-final/source`，摘要为 `eb78474aea6d2a7b5176f910ea95eabdfdb7cc8c0a2e9cf7cd5b0b712b1b9519`。5743项输入与实际普通、竞态Go依赖列表及前后时点一致，三个二进制摘要对应；普通专项十一项含子测试、实际PG一项及SQLite/PG竞态两项通过，普通运行的PG条件跳过由独立PG及竞态命令覆盖。与此前649/109项完整模块回归的生产变化集中在发布运行文件，最终审查应结合该差异、专项及完整Linux/页面组合。正式缓存重新准备会生成新的加密随机数，核对不可变原备份摘要、解密后的完整固定载荷及AAD身份；活动缓存字节的变化需要与授权重新准备记录对应。实际恢复及前端入口读取发布工程师随后提交的 `release-restore-recovery-verified.json`。

恢复最终交付及主协调核对分别为 `.local/evolution/release-restore-recovery-verified.json` 和 `.local/evolution/phase5-release-recovery-root-review.json`。586处引用、603份文件与5743项输入对应通过，正式往返六十项行为、最终恢复二十四项和旧缓存失败后再次启动十二项通过。第六轮完成状态使用正式代次27、实例代次19，实际29组件包含25规则、3连接器配置和1程序。最初备份473/280/280条原版本、审计及检查点逐行保留，独立只读数据库核对在 `.local/evolution/phase5-release-recovered-database-root-review.json`；后续恢复运行采用较晚588/350/350条起点，保留各自时点。原前端继续最终统一构建及页面，Linux第二份清单校验的配置来源读取继续诊断，最终独立审查以对应最终报告为准。

P5-N04已确认实际PG来源登记死锁，主协调证据在 `.local/evolution/phase5-config-deadlock-root-review.json`，锁键5175393874658700308对应 `configuration_connector_source / edge-a/delivery-mqtt:2`。`EnsureConnectorVersions`先Read共享锁再SetEphemeral转排他锁，两个并发调用在同一对象相互等待；原配置工程师负责事务调用及相同条件的PG、race复验，最终Linux包需采用修正后的源码。旧09:19的409也保存同一锁坐标日志，旧数据库对应关系及完整时点读取交付专项检查点；独立审查同时核对来源固定版本、节点版本、权限、反向多引用及正常同时请求。

P5-N04完整交接见 `.local/evolution/phase5-delivery-config-deadlock-checkpoint.json`，主协调对应核对见 `.local/evolution/phase5-config-deadlock-checkpoint-root-review.json`。88处引用、6079项保留输入一致，当前Linux35项通过、1项失败；旧config库OID98437与当前208904已经确认，两次同文档锁死锁均处于各自失败请求等待区间。七个服务均正常退出0且无OOM；原配置工程师已恢复专项修正，最终Linux与十四程序包在其固定版本后继续。

P5-F04同页URL批次切换未刷新当前详情已由 `.local/evolution/phase5-release-navigation-root-review.json` 确认，截图与34次详情请求仍对应先前批次。独立复验同一路由下进入另一批次、返回原批次和清除参数，并核对未提交编辑状态按既有导航规则处理；大型首次断言日志由前端保存无损压缩及原字节摘要对应，浏览器页面与请求记录另有直接保留副本。
