消息基础设施向第五阶段构建交付专家交接

正式部署的 Kafka 4.3.1、NATS 2.14.7 及 NATS 2.12.15 迁移中间版已同步到 `dependencies.json`、`images.lock.json`、`toolchains.json`。NATS 中间版组件键名为 `nats_upgrade_intermediate`，正常 Compose 不启动它，离线迁移流程需要随包提供该锁定镜像归档，或提前按摘要准备可校验镜像。源码标签对应官方实际提交，LICENSE、Kafka NOTICE 和 provenance 已补充。

三个 linux/amd64 镜像的压缩层总和分别为 Kafka 238867857 字节、NATS 11134934 字节、中间版 10885307 字节。目标版在本轮启动前已存在于缓存，本轮新增下载只有中间版的四个压缩层；其压缩负载字节数为 10885307，网络协议、鉴权与重试的传输总量未单独测量。正式镜像和必要依赖传输已经获得用户授权。

部署生成器的消息选版和 broker 协议就绪检查已经统一；`compose.native.json` 与 `compose.site.json` 均通过整份生成结果比较。capacity/site 的 PostgreSQL 18 卷布局、所有声明卷及 Kafka/NATS 目标引用也已执行生成检查。`prepare-site.py` 增加隔离 root/project 参数，并使用可移植相对配置路径；正常文件存储逻辑上限继续为每节点 2GiB。

release 识别验证实际执行现有 `build-release.py` 的容器 inventory、许可证导出、公开文件选择和 manifest 生成流程，编译与包枚举由测试夹具替代。随后实际执行 `verify-release.py`，验证成功并拒绝被修改的锁文件。本轮完整正式发布包、镜像归档及最终组合部署由第五阶段验收；该夹具范围和实际命令保存在 message-deploy-tests 的原始输出及总报告。

Kafka 已验证两种退回条件：元数据仍为 4.0-IV3 时，4.3.1 新增消息可由 4.0.0 继续读取；推进到 4.3-IV0 后，元数据退回请求被拒绝，恢复旧版使用推进前停止状态下取得的独立冷备。最后一次冷备含 42 条消息，目标最终含 45 条，新增 3 条需要重放。恢复源目录必须采用独立路径，并在容器内核对逐文件摘要；Docker Desktop 对替换后的同一路径保留旧目录映射的问题已复现三次，原 BrokerEndpoint v4 错误及诊断资料均保留。

NATS 已完成 2.11.9→2.12.15→2.14.7 的六次逐节点更换，每次均记录服务器实际版本、元数据 leader、成员与复制状态。SIGUSR2 后节点正常退出码为 0，未发生 OOM；旧签名检查点、失效执行权拒绝及重启恢复均通过。全量回退恢复演练应从保存的旧版三节点冷备和身份资料建立独立项目，再核对一致性；本轮完成了旧版冷备生成及旧版冷重启，尚未执行目标版升级后的旧版冷备全量恢复。

受测环境为 aarch64 Docker Desktop 主机，镜像为 linux/amd64，采用架构模拟。Kafka 迁移使用 256MiB broker 堆、2GiB 容器和 96MiB 客户端堆，最大容器峰值为 806240256 字节；这些参数属于有限迁移夹具。正式 runtime 的 Kafka 512MiB 堆、1GiB 容器配置保留现值，完整组合资源验收应使用正式配置另行记录。NATS 夹具每节点 256MiB 容器、192MiB Go 运行时软上限，双前缀预留每节点 6GiB 逻辑存储，冷备实际为约 60KiB。正式资源记录应继续区分逻辑存储预留、文件实际字节、JVM/Go 运行时与容器总体内存。

补充原生用例已用当前代码和独立 CE 4.3.1.6/Kafka 4.3.1 实例通过 `TestNativeCalculatedFieldAlarmAndRetention`，包含平均值 14/3、告警确认/清除重复投影、31 天保留删除、空值与 uint64 最大值传输。命名用例 23.52 秒，CE 峰值 1925427200 字节，Kafka 峰值 584425472 字节；两个容器退出码均为 143，未发生 OOM。此前 Edge 镜像停止信号转发的问题仍按照第一阶段交接列入 E5-04，本轮没有启动 Edge。

全部本轮消息及原生临时服务已结束，PostgreSQL 18 保留给后续专家，原有用户持久实例和卷继续保持原用途。最终清理、当前 PG18 配置、连接状态、磁盘余量与逐项证据路径核对保存在 `.local/evolution/message-infrastructure-verified.json`。
