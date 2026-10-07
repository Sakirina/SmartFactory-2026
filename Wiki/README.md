# SmartFactory Wiki

SmartFactory 把设备协议、现场规则和工厂业务组织在同一套流程中：DataTransfer 采集观测并处理命令，边缘节点保存现场数据和执行记录，云端维护正式定义、人员授权及发布目标，三套页面提供相应工作入口。

本文档面向 1.1 系列。初次体验从[快速开始](getting-started.md)阅读，持久部署从[安装部署](installation.md)阅读；[版本说明](releases/1.1.md)记录组件、支持平台和已经执行的验证范围。下载归档的应用版本、源码和组件摘要由包内清单保存。

## 从安装到完整业务流程

| 阅读入口 | 可以完成的工作 |
| --- | --- |
| [快速开始](getting-started.md) | 启动演示，以温度场景串联数据、模拟、发布、告警和后续处理 |
| [安装部署](installation.md) | 校验归档、准备镜像、初始化单节点或三节点持久环境 |
| [页面与导航](user-guide/console.md) | 使用云端、现场和大屏，选择查询与刷新方式 |
| [五场景教程](tutorials/five-scenes.md) | 核对温湿度、照明、气体、AGV 和计数的输入、规则及反馈 |

## 日常使用

| 阅读入口 | 内容 |
| --- | --- |
| [设备与资产](user-guide/devices-assets.md) | 发现、批准、资产归属与版本，协议设备参数的入口 |
| [数据监控与历史补算](user-guide/data.md) | 服务端分页、精确值、质量与缺口、修订和导出 |
| [分析、告警与联动编排](user-guide/definitions.md) | 节点表单、草稿保存、语义评审、发布编译和历史恢复 |
| [连续模拟、历史分析与后台任务](user-guide/history-tasks.md) | 固定输入、逐点解释、比较、影子候选及任务恢复 |
| [告警处置、工单交接与场景模板](user-guide/business-workbench.md) | 异常周期、责任变更、已阅依据、模板批次及演进 |
| [审批与控制执行](user-guide/control.md) | 会签、现场确认、联锁、原命令核对和剩余步骤恢复 |
| [管理大屏](user-guide/dashboards.md) | 趋势、指标、告警、来源及共享配置 |
| [组织、权限与审计](user-guide/organization.md) | 人员、角色、资源、离线授权及责任记录 |
| [AI 助手与 MCP](user-guide/assistant.md) | 模型配置、实际工具依据、持久调查和客户端连接 |
| [节点配置与分批发布](user-guide/node-releases.md) | 工作负载身份、固定组合、首次转换、批次和失败恢复 |

## 接口与维护

| 阅读入口 | 内容 |
| --- | --- |
| [HTTP API 与实时订阅](api/rest.md) | 登录、类型化查询、续页、SSE 恢复、版本与错误 |
| [数据契约](api/data-contract.md) | 观测、精确数值、消息身份、版本及查询结构 |
| [DataTransfer 设备接入](integration/data-transfer.md) | 协议配置、gRPC、上报策略、命令和进程插件 |
| [配置参考](reference/configuration.md) | 私有部署文件、端口、启动选项及动态参数 |
| [系统架构](architecture/overview.md) | 服务职责、计算、存储、同步及多节点协调 |
| [备份与恢复](operations/backup-restore.md) | 停止写入、数据库与密钥、卷归档、独立恢复和版本迁移 |
| [故障排查](operations/troubleshooting.md) | 安装、权限、消息、任务、执行和发布问题 |
| [消息组件升级](消息组件升级.md) | Kafka 元数据条件与 NATS 三节点升级、配套冷备恢复 |
| [源码构建与发布](development/build.md) | 固定工具链、检查、契约生成、打包和 GitHub Release |
| [扩展开发](development/extensions.md) | 协议进程、组织同步及编排节点 |
| [1.1 版本说明](releases/1.1.md) | 当前能力、组件与验证环境 |
| [1.0 系列版本说明](releases/1.0.md) | 历史标签、当期组件和验证记录 |

各篇文末提供公开源码与契约入口，可据此检查字段、参数和业务条件。Wiki 随部署包同时保存在根目录和 `source/Wiki/`，根目录中的源码链接由打包器改写到包内 `source/`，离线阅读使用相同目录结构。

实际设备运行前，按[组织与权限](user-guide/organization.md)建立人员和资源授权，再按[设备接入](integration/data-transfer.md)核对测点、动作与反馈；参数、工艺阈值及联锁由本次现场配置确定。
