# 故障排查

[Wiki 首页](../README.md) · [配置参考](../reference/configuration.md) · [备份与恢复](backup-restore.md)

排查从出现问题的对象和时刻开始，记录页面身份、节点、业务版本、原请求及错误，再检查所属服务与依赖。操作前保存原日志和私有状态，查询结果、后台任务、配置采用和设备执行分别有对应记录可核对。

## 查看部署服务

在部署包根目录使用安装私有目录，代理和原生 Edge 使用对应 profile：

```sh
SF_STATE='/srv/smartfactory-state'
python3 scripts/bootstrap-runtime.py status --directory "$SF_STATE"
docker compose -f "$SF_STATE/compose.json" \
  --profile native-edge --profile release logs --tail 200 \
  cloud config edge-a edge-a-release-agent edge-a-gateway edge-a-native
```

按实际节点选择服务。日志及 inspect 可以包含私有运行配置，向外部提供时保留时间、请求和错误，处理密码、令牌及密钥内容。

## 安装和入口

| 现象 | 检查内容与后续操作 |
| --- | --- |
| prepare-runtime 拒绝目录 | 指定新的可写目录，核对包内 sf-edge 与 PKI 程序 |
| 镜像缺失 | 核对锁定摘要和本地缓存，完成镜像准备后继续安装 |
| 数据库初始化失败 | 查看 database-initialized.json、tb-bootstrap.json 和 tb-install 日志，核对已完成阶段 |
| 原生同步等待 | 查看 CE／Edge 登录、mTLS 转发、定义队列和原生启动日志 |
| 浏览器 TLS 失败 | 使用 profile 的主机名，核对域名解析和 pki/ca.pem 信任 |
| 安装记录已经存在 | 使用 bootstrap-runtime start 操作该实例 |
| 页面连接到其他服务 | 核对顶部服务模式、节点身份、HTTPS 入口和回环端口 |

初始化脚本保存已完成阶段，重复操作先核对标记与实际数据库。新主机恢复采用备份的空目标流程，完整 UOS 安装恢复的验证范围见[版本说明](../releases/1.1.md)。

## 数据、授权与任务

| 现象 | 检查内容与后续操作 |
| --- | --- |
| 设备有协议数据、业务页面为空 | 核对 approved、device_id、edge_id、配置版本及连接器回执 |
| 趋势没有预期数据 | 核对固定时间、设备测点、粒度、续页、质量和来源 |
| 观测迟到或有缺口 | 阅读 gaps、sources.backfill、修订与历史补算任务 |
| 订阅 reset 或续页失效 | 按原因重新查询，核对身份、授权和游标代际 |
| 401／403 | 重新验证会话、启用状态、角色、资源及依赖对象许可 |
| 409 | 读取最新业务版本并保留输入，核对允许动作后提交 |
| 任务停止处理中 | 查看当前阶段、已提交游标和队列，等待实际终止状态 |
| 逐点进度 100% 仍未完成 | 查看汇总阶段和整个任务状态 |
| AI 调查缺少引用 | 阅读工具失败、预算、过期、删除与当前来源授权状态 |

运行状态中的持久队列需要结合最早消息时间和具体 kind 阅读，tb_entity、tb_definition、tb_telemetry 对应原生投递，业务同步与控制有各自消息及回执。处理原始消息后保持原身份，查询已保存结果和冲突原因。

## 控制和节点发布

| 现象 | 检查内容与后续操作 |
| --- | --- |
| 控制未获批准 | 核对会签人数、组织关系、有效期、指定安全员和现场二次认证 |
| 动作 result_unknown | 读取原 command_id 的持久证据和现场反馈，再核对或恢复剩余步骤 |
| 云端人工操作等待 | 按同一操作身份查看 202 后的边缘受理及终止回执 |
| 配置 restart_required | 安排目标程序重启，核对当前实例的实际采用报告 |
| 明确配置授权拒绝 | 查看当前身份范围及凭据代次，恢复授权后重新取得配置 |
| 发布后续批次等待 | 检查本批节点离线、失败、过期报告及实际消费者 |
| 代理工件核验失败 | 核对清单、工件 SHA-256、节点主密钥与缓存认证 |
| 缓存程序启动失败 | 保存 restore-status、原数据库及密钥，分配修正程序或显式新重试代次 |
| 旧程序数据库不兼容 | 按配套备份恢复到独立目标，并处理备份之后的变化 |

节点心跳与实际运行报告分别保存，报告默认十五秒后失效。临时网络故障可沿已经核验的本地内容恢复，明确授权拒绝清除身份管理的配置缓存；详细采用条件和逐批恢复见[节点指南](../user-guide/node-releases.md)。

本页依据：[初始化脚本](../../scripts/bootstrap-runtime.py)、[查询应用](../../Platform/internal/application/queries.go)、[配置订阅](../../Platform/internal/configcenter/workload_subscriber.go)、[控制状态机](../../Platform/internal/control/control.go)、[发布代理](../../Platform/internal/releaseagent/agent.go)。
