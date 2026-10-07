# 配置参考

[Wiki 首页](../README.md) · [节点配置与发布](../user-guide/node-releases.md)

启动选项确定监听、数据库、身份和组件地址，配置中心管理带 Schema 与版本的程序参数。安装工具生成私有文件，运行程序按工作负载范围读取固定配置并报告实际采用状态。

## 私有文件和地址

| 文件 | 内容 |
| --- | --- |
| `profile.json`、`compose.json` | 包路径、项目、节点、访问地址、服务与数据卷 |
| `deployment-credentials.json` | 初始人员、服务、数据库及原生平台凭据 |
| `development-credentials.json` | 本机工具使用的密码与服务令牌 |
| `workloads.json`、`workload-*.token`、`release-*.token` | 初始身份、普通程序与代理的独立凭据 |
| `release-runtime-edge-*.json` | 代理沿用的数据库、主密钥及组件设置 |
| `edge-*-release-agent/`、`release-artifacts/` | 节点缓存、切换记录及云端工件 |
| `pki/`、`*.master` | CA、证书、私钥、程序加密与签名主密钥 |

| 用途 | 完整部署默认端口 |
| --- | --- |
| 云端页面及大屏 | HTTPS 8443，路径 `/` 与 `/screen` |
| 现场页面 | HTTPS 8444／8445／8446，路径 `/edge` |
| 业务回环调试 | 云端 8090，edge-a 8091、edge-b 8093、edge-c 8094 |
| 原生回环管理 | CE 18080，Edge 18081／18082／18083 |
| 模拟器回环访问 | 19083／19084／19085 |
| 容器内部配置与权威 | 配置 8092，权威及所属节点解析 18445 |
| 云边内部同步 | 业务 mTLS 18443，原生转发 18444 |

本机演示使用 HTTP 8090／8091、配置 8092、模型模拟器 8099 和设备模拟器 18083；完整部署地址读取生成的 `profile.json`。跨主机客户端使用实际服务地址及客户 CA。

## 业务程序启动选项

| 变量或选项 | 用途 |
| --- | --- |
| `SF_LISTEN`／`--listen` | HTTP 监听 |
| `SF_NODE_ID`／`--node-id` | 已登记节点 |
| `SF_DATABASE`／`--database` | PostgreSQL URL、开发 SQLite 文件或 `:memory:` |
| `SF_MASTER_KEY_FILE`／`--master-key` | 加密和审计签名主密钥 |
| `SF_STATIC_DIR`／`--static` | 页面静态目录 |
| `SF_CONFIG_URL`／`--config-url` | 独立配置中心 |
| `SF_CONFIG_TOKEN_FILE`、`SF_WORKLOAD_BOOTSTRAP_FILE` | 当前工作负载凭据及初始登记 |
| `SF_CONFIG_TLS_CA`、`SF_CONFIG_TLS_CERT`、`SF_CONFIG_TLS_KEY` | 配置 TLS 身份 |
| `SF_CONFIG_AUTHORITY_URL`、`SF_AUTHORITY_LISTEN` | 权威校验与所属节点解析监听 |
| `SF_AUTHORITY_TLS_CA`、`SF_AUTHORITY_TLS_CERT`、`SF_AUTHORITY_TLS_KEY` | 权威监听的 TLS |
| `SF_RELEASE_ARTIFACT_ROOT` | 云端可写工件目录 |
| `SF_DATATRANSFER_ADDRESS`／`--datatransfer` | 采集 gRPC 地址 |
| `SF_DATATRANSFER_CA`、`SF_DATATRANSFER_CERT`、`SF_DATATRANSFER_KEY` | 跨主机采集 TLS |
| `SF_BOOTSTRAP_PASSWORD`、`SF_SERVICE_TOKEN` | 初次管理员密码和服务通信令牌 |
| `SF_TB_URL`、`SF_TB_USERNAME`、`SF_TB_PASSWORD`、`SF_TB_CALLBACK_URL` | 原生平台地址、认证与回调 |
| `SF_SYNC_URL`、`SF_SYNC_LISTEN`、`SF_CLOUD_SIGNING_KEY` | 云边同步及可信签名公钥 |
| `SF_TLS_CA`、`SF_TLS_CERT`、`SF_TLS_KEY` | 云边 mTLS |
| `SF_NATS_URL`、`SF_NATS_TOKEN`、`SF_NATS_PREFIX` | 三节点协调连接及隔离前缀 |
| `SF_NATS_CA`、`SF_NATS_CERT`、`SF_NATS_KEY` | NATS 客户端身份 |
| `SF_NATIVE_RELAY_LISTEN`、`SF_NATIVE_RELAY_TARGET` | 原生云边转发 |

显式命令行选项覆盖对应环境默认值。`--seed` 装入示例，`-build-info` 输出版本、来源与兼容范围；初次管理员密码至少十二字符。发布代理的参数见 [sf-release-agent](../../Platform/cmd/sf-release-agent/main.go)，正式安装使用生成的运行配置。

## 常用参数

| 标识 | 默认值或内容 |
| --- | --- |
| `heartbeat.interval_ms`／`heartbeat.offline_ms` | 5000／15000 毫秒 |
| `identity.offline_ttl_ms` | 172800000 毫秒，48 小时 |
| `control.approval_ttl_ms`／`control.start_ttl_ms` | 300000／10000 毫秒 |
| `control.confirmations` | 普通 1 工程师和 1 领导，强制 2 工程师和 1 领导 |
| `ui.refresh_ms` | 1000 毫秒 |
| `queue.capacity` | 200000，队列水位计算容量 |
| `queue.watermarks` | 提醒 60%、警告 80%、错误 95%、恢复 50% |
| `storage.auto_backfill_days` | 30 天 |
| `storage.retention` | raw 30、minute 90、hour 365、day 与 alarm 1095、quarantine 7 天 |
| `storage.archive` | 启用，hot_hours 24、block_points 4096、blocks_per_run 16 |
| `notification.email`／`notification.sms` | 默认停用，渠道参数与加密凭据 |
| `ai.model` | provider、api、stream、endpoint、model、api_key、timeout_ms |

`storage.retention` 使用 `raw_days`、`minute_days`、`hour_days`、`day_days`、`alarm_days`、`quarantine_days`，完整字段与允许范围读取条目 Schema。水位满足恢复小于提醒、提醒小于警告、警告小于错误，汇总保留期随粒度增加。

1. 在“配置中心”按 Schema 编辑，凭据条目加密保存。
2. 保存新版本后查看目标范围和采用状态，动态参数核对实际有效值。
3. 静态参数显示 `restart_required` 时安排程序重启，再读取当前实例报告。

本页依据：[启动实现](../../Platform/internal/app/app.go)、[参数 Schema](../../Platform/internal/configcenter/policy.go)、[配置条目](../../Platform/internal/configcenter/config.go)、[运行策略](../../Platform/internal/store/policy.go)、[部署生成器](../../scripts/prepare-runtime.py)。
