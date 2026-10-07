# 备份、恢复与数据库迁移

[Wiki 首页](../README.md) · [安装部署](../installation.md) · [消息组件升级](../消息组件升级.md)

完整恢复采用同一时点的数据库、私有配置、证书与密钥，并保存消息服务和采集状态。备份工具提供 PostgreSQL custom 导出、SQLite 一致性副本、文件清单与摘要校验；运维人员先停止写入，再保存和核对配套内容。

## 备份范围

| 内容 | 完整部署位置 |
| --- | --- |
| 云端业务、配置、CE | cloud-db 的 smartfactory、sf_config、thingsboard 数据库 |
| 各节点业务、原生 Edge | edge-*-db 的 smartfactory、tb_edge 数据库 |
| 采集、模拟与代理 | 私有节点目录、edge-*-release-agent 目录 |
| 身份和运行设置 | 整个私有目录，包含 CA、证书、令牌、主密钥、profile 与 Compose |
| 工件和发布记录 | release-artifacts、业务库中的固定目标与报告 |
| 消息及原生目录 | Kafka 卷、各 Edge 原生数据卷、site 的三个 NATS 卷及配置 |

备份记录包含包版本、清单、停机时间、数据库版本和实际卷名。以下为单节点 capacity 示例，三节点逐个增加数据库与原生卷，并加入三个 NATS 卷归档。

## 建立一致备份

1. 在维护时段停止实例，确认程序及代理子进程退出，再只启动导出需要的数据库。示例私有目录和备份目录按安装位置替换：

   ```sh
   SF_STATE='/srv/smartfactory-state'
   python3 scripts/bootstrap-runtime.py stop --directory "$SF_STATE"
   docker compose -f "$SF_STATE/compose.json" \
     --profile release start cloud-db edge-a-db
   ```

   等待数据库就绪，业务、原生平台、采集和模拟继续停止，确保数据库写入者及设备动作已经停止；`--quiesced` 表示本次备份已经完成该操作。

2. 在私有目录之外准备新的暂存目录，保存私有文件与包清单：

   ```sh
   SF_BACKUP_ROOT='/srv/smartfactory-backups'
   mkdir -p "$SF_BACKUP_ROOT"
   SF_STAGING="$SF_BACKUP_ROOT/snapshot-input"
   mkdir -m 700 "$SF_STAGING"
   tar -C "$SF_STATE" -cf "$SF_STAGING/private-state.tar" .
   cp release-manifest.json "$SF_STAGING/release-manifest.json"
   ```

3. 从 Compose 项目标识和容器挂载核对实际卷名，归档已停止的 Kafka、Edge 原生数据和 NATS。下面处理一个卷，分别替换卷名和输出标签：

   ```sh
   SF_RUNTIME_IMAGE=$(python3 -c 'import json; print(json.load(open("deploy/images.lock.json"))["images"]["runtime"]["pinned"])')
   SF_VOLUME='REPLACE_WITH_ACTUAL_VOLUME_NAME'
   SF_VOLUME_LABEL='kafka'
   docker volume inspect "$SF_VOLUME"
   docker run --rm --pull=never --network none --read-only \
     --mount "type=volume,source=$SF_VOLUME,target=/source,readonly" \
     --mount "type=bind,source=$SF_STAGING,target=/backup" \
     "$SF_RUNTIME_IMAGE" \
     tar -C /source -cf "/backup/$SF_VOLUME_LABEL.tar" .
   ```

4. 取得数据库容器标识并生成新的备份。示例要求暂存目录已经含 kafka.tar、edge-a-native-data.tar：

   ```sh
   SF_CLOUD_DB=$(docker compose -f "$SF_STATE/compose.json" --profile release ps -q cloud-db)
   SF_EDGE_DB=$(docker compose -f "$SF_STATE/compose.json" --profile release ps -q edge-a-db)
   SF_BACKUP="$SF_BACKUP_ROOT/snapshot-01"
   python3 scripts/backup-state.py create \
     --output "$SF_BACKUP" \
     --quiesced \
     --postgres "cloud-business=$SF_CLOUD_DB,smartfactory,postgres" \
     --postgres "cloud-config=$SF_CLOUD_DB,sf_config,postgres" \
     --postgres "cloud-native=$SF_CLOUD_DB,thingsboard,postgres" \
     --postgres "edge-a-business=$SF_EDGE_DB,smartfactory,postgres" \
     --postgres "edge-a-native=$SF_EDGE_DB,tb_edge,postgres" \
     --file "private-state.tar=$SF_STAGING/private-state.tar" \
     --file "release-manifest.json=$SF_STAGING/release-manifest.json" \
     --file "kafka.tar=$SF_STAGING/kafka.tar" \
     --file "edge-a-native-data.tar=$SF_STAGING/edge-a-native-data.tar"
   python3 scripts/backup-state.py verify "$SF_BACKUP"
   ```

   每个条目名称唯一，输出目录必须尚未存在。独立 SQLite 文件可增加 `--sqlite NAME=PATH`，工具使用一致性副本并记录表数量和文档版本。

5. 核对清单、加密文件与所需卷均已保存，再启动原实例并检查：

   ```sh
   python3 scripts/bootstrap-runtime.py start --directory "$SF_STATE"
   python3 scripts/bootstrap-runtime.py status --directory "$SF_STATE"
   ```

## 在独立目标恢复

1. 核验并复制备份到新目录：

   ```sh
   python3 scripts/backup-state.py verify /srv/smartfactory-backups/snapshot-01
   python3 scripts/backup-state.py restore \
     /srv/smartfactory-backups/snapshot-01 \
     --output /srv/smartfactory-restore
   ```

2. 准备备份对应的程序与锁定镜像，展开私有归档。恢复副本中的 profile 包路径、Compose 的 bind 源路径按实际主机调整，Compose 项目名称与 profile.project 同步设为独立项目，选择独立端口及新卷；保留原节点身份、CA 和主密钥。
3. 只启动空 PostgreSQL 目标，等待数据库就绪；每个目标数据库已经存在且无业务表，再逐项执行：

   ```sh
   python3 scripts/backup-state.py restore-postgres \
     /srv/smartfactory-backups/snapshot-01 \
     --name cloud-business \
     --container REPLACE_WITH_TARGET_DATABASE_CONTAINER \
     --database smartfactory \
     --user postgres
   ```

   工具检查空库并使用目标容器的 pg_restore，以单事务恢复；按备份清单继续恢复配置、CE 和所有 Edge 数据库。

4. 保持消息及原生服务停止，将归档展开到各自独立目标卷，核对权限、实际挂载源和文件摘要，再还原采集与代理状态。
5. 确认数据库、文件与身份配套后，使用恢复私有目录的 `bootstrap-runtime.py start` 启动原生组件、业务、代理及采集；保留的 installed.json 用于已经安装实例的启动。
6. 核对实体、规则、参数、人员许可、审计、同步游标、任务及队列。控制结果未知时核对原命令与现场动作，再处理备份之后的数据、动作及检查点。

## PostgreSQL 16 到 18

当前 PostgreSQL 18.6 使用 `cloud-db-pg18`、`edge-a-db-pg18` 等新主版本卷，挂载 `/var/lib/postgresql`，实际数据目录为其下 `18/docker`。升级采用逻辑导出和空目标恢复，原 PostgreSQL 16 卷保留供恢复旧实例。

1. 停止所有写入并保存 PostgreSQL 16 的全部业务、配置及原生库，以及对应私有文件、卷名和版本。
2. 在独立配置中选择锁定 PostgreSQL 18 镜像、新项目、新端口和 `*-pg18` 卷，使用备份的身份及主密钥。
3. 创建空目标库，逐库执行 restore-postgres，使用目标角色和连接凭据。
4. 启动业务，完成 Goose 编号迁移、River 迁移和任务映射，核对历史文档、精确观测、归档、审计、游标和待处理任务；再验证新增、读写与归档。
5. 原生 CE／Edge 完成相应数据库升级及通信检查后切换访问入口，保存日志与备份；恢复旧环境时停止目标写入，并处理切换期间新增的数据。

## 发布及消息恢复

代理目录中的 active.json、pending.json、last-transition.json 保存已运行发布、待完成切换和实际进程；restore-status.json 保存核验后启动失败的任务与代次。恢复时把这些文件与原数据库、主密钥、工件配套保存，核对旧程序的数据库范围。

缓存程序启动失败后，通过[正式工件与批次](../user-guide/node-releases.md)分配修正程序或新重试代次，逐批检查实际运行。Kafka 元数据推进前后采用不同恢复条件，NATS 从三个配套目录及原 TLS、路由和签名资料恢复，具体次序见[消息组件升级](../消息组件升级.md)。

本页依据：[备份工具](../../scripts/backup-state.py)、[启停及转换](../../scripts/bootstrap-runtime.py)、[卷与目录生成](../../scripts/prepare-runtime.py)、[代理恢复](../../Platform/internal/releaseagent/agent.go)、[数据库迁移](../../Platform/internal/store/migrations.go)。
