# 部署与运维

## 部署配置

目标系统为完整安装的 UOS Server V20 x86_64，初始虚拟机配置为 8 vCPU、16GB 内存、220GB 磁盘。容量配置包含一个边缘节点，联动配置包含三个边缘节点；每个节点使用独立 PostgreSQL、DataTransfer 状态目录、模拟设备状态和 ThingsBoard Edge 数据。配置生成器固定镜像摘要，私有目录保存每个客户的 CA、节点证书、主密钥和随机凭据。

`capacity` 的 fleet 模拟模式运行 500 台设备，每台每 3 秒产生烟雾、排风和联锁三个值，合计平均 500 点/秒；`site` 运行三个节点，每节点 50 台设备，每 5 秒更新一次。A04 的 200 Modbus、200 MQTT、100 OPC-UA 高吞吐组合由 `dt-benchmark` 单独启动。五场景演示使用 `capacity --simulation scenes`。

托管与自托管共用应用组件和加密机制，配置清单分别标记运维归属。云端控制台默认 HTTPS 8443，边缘现场入口为 HTTPS 8444 至 8446；原生管理端口和业务调试端口绑定本机回环地址。外部访问使用与域名匹配的证书，客户 CA 可由现有 PKI 替换。

## UOS 安装流程

1. 记录 UOS ISO 完整版本、SHA-256、内核、CPU、内存、磁盘、Docker 和 Compose 版本。复制 Linux x86_64 交付包，执行 `python3 scripts/verify-release.py <交付包目录>`，按镜像锁定清单准备镜像。
2. 执行 `python3 scripts/prepare-runtime.py --bundle <交付包目录> --directory <新的私有状态目录> --profile capacity --hosting self --public-host <访问域名>`。联动配置将 profile 设为 `site`；托管配置将 hosting 设为 `hosted`。准备脚本生成证书、独立密钥与 Compose，并验证配置语法。
3. 执行 `python3 scripts/bootstrap-runtime.py install --directory <私有状态目录>`。脚本安装 ThingsBoard 数据库、创建专属租户，登记节点证书与公钥，等待签名离线权限包和原生定义投影；原生平台就绪后启动模拟器与采集，批准模拟物理设备，建立工厂资产归属，并发布示例联动预案。
4. 在浏览器信任客户 CA 后访问生成的 HTTPS 地址，使用私有目录中的初始账号信息登录。测试结束后按实际组织导入人员和授权，修改示例安全员的二次认证资料。
5. 使用 `bootstrap-runtime.py stop` 和 `start` 检查正常停机与恢复，使用 `status` 查看组件状态。完成部署后再执行目标系统上的 A01 至 A18，并将证据绑定到交付清单中的二进制摘要。

本机短期功能演示可使用 `python3 scripts/run-memory-demo.py --duration 900`，云端、边缘、配置、采集与模拟设备的测试数据库位于内存中，最长运行一小时后自动结束。此模式用于页面、模型模拟器和五场景回归；原生 ThingsBoard 联调通过完整部署运行。

本机完整原生联调的 PostgreSQL 与 Kafka 测试数据可放入有容量限制的 tmpfs，同时统计容器、Docker 虚拟机和主机可用内存，保留至少 2GiB 主机余量。此次 8GiB Docker 虚拟机在演示完成后发生过全局内存不足，原生云服务自动恢复；[事件和复验记录](../evidence/native-post-recording-resource-event.json)随测试报告保存。持续运行时按整个部署的实际峰值安排内存与数据空间，达到测试时限后停止采集并释放临时环境。

## 配置与运行状态

| 配置 | 默认行为 |
|---|---|
| 队列水位 | 60% 提醒、80% 警告、95% 错误，50% 恢复 |
| 心跳与离线 | 5 秒心跳，15 秒判定离线 |
| 数据时效 | 三个采样周期，最低容差 5 秒，预案可单独配置 |
| 会签和启动 | 会签有效 5 分钟，正式下发后 10 秒内启动 |
| 离线权限 | 默认 48 小时，联网时更新 |
| 页面更新 | 默认一秒，页面允许选择刷新方式 |
| 热数据与归档 | 24 小时热数据，4096 点一块，每轮最多 16 块；完整原始保留 30 天 |
| 自动补算 | 30 天内自动处理，更早资料通过异常任务检查 |

程序启动参数使用环境变量和命令行传入，运行中参数通过配置中心 Schema 校验和版本订阅应用。静态参数显示待重启，动态参数显示各节点实际生效版本与失败原因。DataTransfer 跨主机连接可设置 `SF_DATATRANSFER_CA`、`SF_DATATRANSFER_CERT`、`SF_DATATRANSFER_KEY`；共享节点网络中的回环 gRPC 连接使用本地端口。

容量规划应同时计算业务热数据、压缩归档、TB 投影、未完成同步、修订、索引、WAL 和备份。当前 [容量报告](storage-and-io.md) 给出观测部分的实际样本和外推，完整 24 小时测量将在 UOS 环境执行。持续监测磁盘余量、积压数量、归档进度、数据缺口和同步时间，空间不足时先定位未确认事件与未完成补传。

## 备份与恢复流程

1. 停止业务写入进程、采集和模拟设备，等待数据库完成当前提交，保留 PostgreSQL 容器供备份使用。记录原生 CE/Edge 和 NATS 数据的同一停机时点。
2. 执行 `scripts/backup-state.py create --output <新备份目录> --quiesced`，用 `--postgres 名称=容器,数据库,用户` 添加数据库，用 `--sqlite 名称=路径` 添加采集或模拟器状态，用 `--file 名称=路径` 添加主密钥、CA、证书和私有部署配置。每个输入名称须唯一。
3. 执行 `backup-state.py verify <备份目录>` 核对摘要、SQLite 完整性、表数量和文档版本。PostgreSQL 使用压缩 custom dump，原生平台数据库也按该方式备份；NATS 停机后的三个数据卷与其配置一同备份。
4. 恢复到新的目录和空数据库，分别使用 `restore` 与 `restore-postgres`。程序拒绝覆盖已有业务表的 PostgreSQL 目标，恢复时继续使用备份中的加密和签名密钥。
5. 启动后检查实体、规则、人员权限、参数、审计链和未完成任务，检查相同控制 ID 不产生新设备动作、旧配置版本被拒绝，并核对云边同步游标和待投事件。

## 现场故障复验

`scripts/prepare-site.py --memory` 准备三个采用 mTLS 的 NATS 测试实例；`scripts/check-site.py --faults 100` 启动三个独立应用进程、三个网关和 150 台 MQTT 模拟设备，依次执行多数失联、单副本失效、协调进程退出及参与进程暂停。测试数据库位于有容量限制的 tmpfs，应用进程恢复从仍在运行的数据库读取已有状态，报告单独标记完整 24 小时验收状态。

实际磁盘持久性由小规模提交失败、数据库重开、备份恢复和 A06 事件测试验证；系统重启与长时间离线测试在 UOS 的持久部署配置中进行。

## 连续运行验收流程

四种配置的生成结果和验收计划已经通过[语法与结构检查](../evidence/deployment-profile-matrix.json)。完整测量使用 UOS 的持久数据卷；脚本记录 ISO、内核、发布清单摘要、每次采样的数据库大小、任务积压、页面可达性、权限与审计状态。`--plan-only` 可在实际执行前查看时长和操作范围。

1. 对容量部署运行 `python3 scripts/check-long-runtime.py --directory <私有状态目录> --mode offline --output <报告路径> --plan-only`，核对 24 小时断云与最长六小时恢复时段；同样检查 `--mode storage` 的存储计划。
2. 在 UOS Docker 主机安装并核对 `tc` 与 `nsenter`，使用管理员权限执行 `python3 scripts/check-long-runtime.py --directory <私有状态目录> --mode offline --output <A08报告路径> --uos-iso-version <完整版本> --uos-iso-sha256 <ISO摘要>`。脚本暂停云端业务、配置和原生云平台，持续检查边缘采集、本地页面、既有授权、动作反馈与审计；恢复时对云边流量设置 100Mbps 和往返 20ms 的链路条件，完成后移除本次设置。
3. 对联动配置执行相同的断云命令，并结合目标 UOS 上 `check-site.py --faults 100 --output <现场故障报告>` 的结果评定 A09。持续运行报告单独保存实际时长，完整 A09 还需核对故障报告及三个节点的独立数据和运行状态。
4. 执行 `python3 scripts/check-long-runtime.py --directory <私有状态目录> --mode storage --output <A12报告路径> --backup-output <新的备份目录> --uos-iso-version <完整版本> --uos-iso-sha256 <ISO摘要>`，运行至少 24 小时；脚本汇总业务与原生数据库的表、索引和空间占用，并在暂停写入后生成备份和实际备份大小。
5. 阅读报告中的实际负载、缺口、积压、恢复时间与错误字段，将完整结果补入需求追踪表。需要短期排查时显式添加 `--development-smoke --duration-seconds 60 --recovery-seconds 60 --sample-seconds 5`，短期结果保留独立标记。

实际 24 小时容量测试按 500 点/秒提供负载。开始前根据可用空间与已有容量样本确认预算，测试运行中保留至少 5GiB 可用磁盘；内存测试模式用于功能和查询排查，持久部署承担当期存储与恢复测量。
