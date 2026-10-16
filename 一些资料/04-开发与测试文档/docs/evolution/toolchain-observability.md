# 工具链、维护镜像与统一观测

实施日期为 2026-10-04。集中选版读取 `deploy/toolchains.json`；其中 `target` 表示计划目标，`locked` 表示当前构建或部署引用，`validation` 记录实际完成的验证。独立目标镜像存放于 `deploy/evolution-targets.lock.json`，当前部署使用 `deploy/images.lock.json`。PostgreSQL、Kafka、NATS 的后续目标已经取得，当前部署分别引用 PostgreSQL 16、Kafka 4.0.0、NATS 2.11.9；第二阶段的数据迁移与运行配置验收完成后，再更新当前锁定清单。

| 组件 | 当前选版或目标 | 实际验证 | 后续工作 |
| --- | --- | --- | --- |
| Go | 编译器 1.27.1，模块最低 1.27.0，toolchain go1.27.1 | 本机与构建镜像版本一致；DataTransfer 全模块测试及 Linux amd64 构建；Platform 指定包测试、九个 Linux amd64 cmd 构建及两个原生应用运行 | 汇合后的 Platform 全量回归及最终发布包 |
| Node | 24.21.0 LTS，npm 11.19.0 | 官方 SHA-256、实际执行、前端类型检查与构建 | 第二阶段 Vite/TypeScript 组合 |
| ThingsBoard CE/Edge | 4.3.1.6 / 4.3.1.6EDGE | 官方源码提交、镜像摘要与软件包版本；PG18 上的双端回调、新旧消息、重复处理、原始及计算遥测、审计校验 | 第二阶段旧数据迁移组合 |
| Envoy | 1.39.1 | 实际 TLS、可信客户端 mTLS、缺失与不可信客户端拒绝、应用认证响应和逐帧 SSE | 最终部署环境的对应上游配置 |
| PostgreSQL | 当前 16；目标 18，实际目标镜像 18.6 | 编号迁移与旧记录保留；CE/Edge 在独立 PG18 数据库运行 | 16→18 规定规模数据迁移及存储并发专项 |
| Kafka | 当前 4.0.0；目标 4.3.1 | 实际版本；临时 broker 支持 CE/Edge 原生双端通信 | 持久队列迁移、重启及恢复验证 |
| NATS | 当前 2.11.9；目标 2.14，实际目标镜像 2.14.7 | 镜像摘要与实际版本 | JetStream 与 mTLS 集群 |

ThingsBoard CE 源码 tag `v4.3.1.6` 对应 `c77335f4a524bb9fe0209806672c6ec7aea76141`，Edge 对应 `634b1852d2479983e2eb9aafcfaecbc164220f63`。Go 1.27.1 的 alpine3.22 构建标签未发行，因此使用实际存在的 `golang:1.27.1-alpine`。官方元数据还记录了 ThingsBoard 4.4 和 NATS 2.15，本轮按照已经确定的维护目标完成验证；Alpine 3.22 当前官方摘要与既有锁定摘要一致，证据为 `.local/evolution/runtime-image-current.json`。

`python3 scripts/check-toolchains.py` 同时校验两个 Go 模块、版本文件、CI 读取入口、构建镜像、镜像摘要及层尺寸、Compose 引用、前端 package 与 lock，并执行本机 Go/Node/npm/Python 版本采集；`--configuration-only` 用于尚未安装工具的配置检查。`toolchain.environment(root, base)` 选择匹配当前主机架构的工作区 Node，并设定 `GOTOOLCHAIN=local`；`toolchain.preflight(root, environment)` 返回 selected/observed，存在差异时在发布产物创建前抛出错误。主智能体已把两个函数加入既有 build-release.py，发布 manifest 保存同一环境中的实际工具版本。

受测二进制记录在 `.local/evolution/go-build-verification.json`，每个条目保存 Go 嵌入的编译器、GOOS/GOARCH/CGO、入口包、文件时间与 SHA-256。DataTransfer Linux amd64 摘要为 `1097f7c2459f787a24f8e4d472e0ed79279ffd353c1fa1d529d9a80ce1e0c7e2`；用于本轮原生运行的 Darwin arm64 sf-cloud 摘要为 `96d991533fa895b25cd8a957a85f605c7f5512b8baf5de83f523109d60a3c408`，sf-edge 摘要为 `6e4eb457606467cc0022c5d3f2ebabfcd30109650694eb9635ab48e58f887d3c`。这些构建对应各条目注明的代码时间；Platform 首批测试范围为 `./internal/application/... ./internal/api ./internal/store ./internal/engine ./pkg/contracts ./cmd/sf-contracts ./internal/app`，输出和源文件摘要由 `.local/evolution/architecture-contracts-verified.json` 保存，最终代码的全量回归由后续审查完成。

共享观测实现为 `DataTransfer/observability`，Platform 的 `internal/observability` 提供同一接口。`New(ctx, Config)` 建立资源、TracerProvider 和 MeterProvider，`Install()` 在应用启动阶段执行，关闭时统一 `Shutdown()`。`StartOperation(ctx, operation, Identity)` 返回派生 context 与只执行一次的 `finish(error)`；`WithIdentity` 合并请求、消息、命令、调用者、草稿、定义和版本标识，`LogAttrs` 取得相同业务标识及 trace/span ID。指标仅带 operation/outcome 标签，错误正文通过固定异常说明导出。

应用 HTTP 入口生成或校验最长 128 字节的 X-Request-ID，将规范值放入请求与响应；W3C TraceContext/Baggage 通过 HTTP 和 gRPC 传播，再次 ExtractHTTP 会保留已有本地 span。HTTP 观测使用 httpsnoop 保留流式 ResponseWriter 接口，拒绝响应与 panic 同样结束 span。应用保存、校验、发布，以及 Store.Write，分别形成用例和事务 span。实际 OTLP protobuf 导出测试验证 HTTP→草稿用例→事务在相同 trace 中，发布事务以发布用例为父级，并核对调用者、草稿、定义、请求及版本；本轮专项输出 `.local/evolution/app-otel-final.log`，真实浏览器证据为 `.local/evolution/pilot-verified.json`。DataTransfer 全模块测试、观测与 bootstrap 的 race 检查分别保存于 `.local/evolution/datatransfer-tests-final.log`、`.local/evolution/observability-race-final.log`。

默认 traces/metrics exporter 为 none，设置 `OTEL_TRACES_EXPORTER=otlp`、`OTEL_METRICS_EXPORTER=otlp` 和 `OTEL_EXPORTER_OTLP_ENDPOINT` 后启用标准 HTTP 导出；统一端点追加 `/v1/traces` 与 `/v1/metrics`。采样器遵循 OTEL_TRACES_SAMPLER/ARG，超时和指标间隔均校验正值，带凭证或查询串的端点会被拒绝。

Envoy 六项实际通信记录位于 `.local/evolution/envoy-runtime-verification.json`，最终 TLS 健康响应在 14:49:10.119 UTC 返回 200，SSE 三帧分别在 0、1.002、2.004 秒到达。启动时的两次 EOF 保存为带时间的 startup_probe_attempts，最终健康结果独立保存。独立验证通过 Docker 的 host.docker.internal 访问绑定 IPv4 的原生应用；默认地址选择曾尝试不可达的 IPv6 上游，验证 cluster 改为 V4_ONLY 后路由通过。该条件与正式 Compose 的 Docker 内部服务名分别记录，正式部署需要依据其上游地址支持情况选择 DNS 地址族。

原生组合曾发生资源故障：PG18 在 14:23:07.913 UTC 首次报告连接耗尽，14:23:07.987 UTC 首次因 cgroup OOM 出现后端 signal9，随后完成 WAL 恢复。原来 512 MiB PG 容器和两个最大 16 连接的 Hikari 池调整为 PG 1 GiB、每端最大 4 连接、最少空闲 1 连接，max_connections 保持 30。Kafka 的临时 broker 在 14:40:31 UTC 发生 OOM 后重新创建，临时队列重新初始化；此前已经保存的平台与 TB 消息、计算遥测及重复处理记录经过独立查询和审计校验。CE 的 2.5 GiB 容器在 14:51:55 UTC 再次发生 OOM，实际启动脚本采用 JAVA_OPTS，配置未覆盖声明的堆参数；这次失败推动了下述有限预算验证。完整时间、配置与 Docker OOM 事件见 `.local/evolution/native-resource-repair.json`。

修复后的有限验证把 CE 设置为 512 MiB 堆、256 MiB metaspace、64 MiB direct memory、64 MiB code cache，容器为 3 GiB；Edge 容器 1.75 GiB、Kafka 1 GiB、PG18 1 GiB，组合上限 6.75 GiB，Docker 可用上限约 7.75 GiB。Docker 主机为 aarch64，CE/Edge 镜像为 linux/amd64，运行使用架构模拟。NMT 记录 JVM 的 committed 为 867,282,944 字节、Java heap committed 为 384,827,392 字节，CE cgroup 峰值为 1,854,763,008 字节；报告保留两种计量，尚未测量的占用来源留待专项诊断。

CE 在 84.25 秒完成启动，受控流程运行 164.019 秒，执行两端原有消息与重复检查、各一条新消息与重复、平台及 TB 遥测、audit valid=true，以及单并发 12 次认证 API 读取。该轮 OOMKilled=false，最终 SIGTERM 退出 143；证据为 `.local/evolution/native-controlled-resources.json`、`.local/evolution/native-controlled-roundtrip.json`。第一轮八秒遥测查询探测超时的记录保存在 `.local/evolution/attempt1-native-controlled-resources.json`，第二轮采用三十秒请求等待。

本轮原生应用、CE、Edge、Kafka 和 Envoy 临时服务已经结束，临时网络已删除，原有九个持久容器保持原先停止状态。应用进程退出 0，CE 和 Kafka 退出 143；Edge 在 docker stop 三十秒时限后退出 137，该时段没有新的 Edge OOM 事件。Edge 镜像 PID1 为 bash、Java 为子进程，镜像启动脚本为 `/usr/bin/start-tb-edge.sh`，后续需要验证停止信号转发或直接向 Java 发送 TERM 的部署方式，具体入口与 E5-04 验收流程见 `handoff-edge-stop.md`。停止命令结果与实际容器 die 事件分别保存于 `.local/evolution/service-cleanup.json`。

PG18 继续运行供后续工程师使用，2 CPU、1 GiB 内存、512 MiB tmpfs，实际版本 18.6，max_connections=30、shared_buffers=64MB、work_mem=4MB。清理后观测 331.7 MiB，SQL 探测期间只有该探测本身一个客户端；凭据单独保存在权限 0600 的 `.local/evolution/pg18-test.json`。工作卷可用 2,201,440,256 字节，至少保留 1 GiB；/private/tmp 和 Docker 所在存储池可用 43,241,902,080 字节。构建、临时状态与发布归档使用 /private/tmp，证据为 `.local/evolution/post-cleanup-resources.json`。

必要依赖与镜像传输已经获得用户授权。八个目标镜像的去重复压缩层共 2,588,699,400 字节，现有层比较估计新增 2,551,227,137 字节；Node 官方归档 52,909,993 字节，SHA-256 验证后已经删除本轮归档。本次共享 Go 缓存统计为 127 个 zip、242,582,842 字节，其中包含其他智能体下载的依赖。镜像清单尺寸和缓存字节数用于核对取得内容，网络重试及协议开销未由 CLI 单独计量。

验收入口为 `.local/evolution/toolchain-observability-verified.json`。E1-01 保存明确的实际构建与测试范围，E1-02 的双端原生通信、TLS/mTLS 与流式路由有限回归已经通过，E1-03 的工具链与镜像来源清单一致，E1-07 的实际应用观测已经通过。第二阶段存储与 River 的已保存代码、锁顺序及未完成验收位于 `handoff-storage.md`、`storage-lock-order.md`，专职工程师继续完成真实 PG 并发、归档计算外移、精确集合 CAS 与 River 正式任务替换。
