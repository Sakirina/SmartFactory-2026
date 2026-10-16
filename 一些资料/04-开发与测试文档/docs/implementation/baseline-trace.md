# 基线逐项对应

本表按原文编号提供实现与验证入口，具体通过状态、受测构建和剩余目标环境工作见 [A01 至 A18 追踪表](requirements.md) 与 [本机构建回归](../evidence/go-regression-final.json)。多段需求的完整内容保留在所链接的基线文件中；本轮变更依据见 [方案调整记录](decisions.md)。

## 平台功能基线

来源：[智慧工厂云端平台需求基线.md](../../Baseline/Platform/智慧工厂云端平台需求基线.md)。

| 编号 | 原文内容摘要 | 实现入口 | 验证入口 | 对应验收 |
|---|---|---|---|---|
| FR-GW-001 | 云端平台与边缘控制节点之间的一切数据交互（上行接入、下行控制、配置推送），均须经由平台数据网关微服务收发，其它云端服务不得绕过网关直连边缘组件 | [cloudsync](../../Platform/internal/cloudsync) | [exchange_test.go](../../Platform/internal/cloudsync/exchange_test.go) | A06、A08、A10、A14 |
| FR-GW-002 | 平台数据网关不得直连边缘侧的「数据传递微服务」——后者是边缘控制节点的内部组件 | [cloudsync](../../Platform/internal/cloudsync) | [exchange_test.go](../../Platform/internal/cloudsync/exchange_test.go) | A06、A08、A10、A14 |
| FR-OC-001 | 编排与控制中心下行的内容分为两类，二者均停留在业务抽象层，不含设备级协议帧 | [control](../../Platform/internal/control) | [control_test.go](../../Platform/internal/control/control_test.go) | A07、A09、A14 |
| FR-OC-002 | 指令级 command_id 由边缘命令编排引擎分配，云端不介入指令级生命周期的标识 | [control](../../Platform/internal/control) | [control_test.go](../../Platform/internal/control/control_test.go) | A07、A09、A14 |
| FR-GW-003 | 平台数据网关接入边缘上行数据时，须直接消费边缘侧已冻结的 DeviceMessage（Protobuf 信封，v2.3 基线），不得在云端另立一套并行的数据… | [datatransfer](../../Platform/internal/datatransfer) | [bridge_test.go](../../Platform/internal/datatransfer/bridge_test.go) | A02、A03、A06 |
| FR-GW-004 | 网关与各云端服务须沿用信封中已有的关联键 | [datatransfer](../../Platform/internal/datatransfer) | [bridge_test.go](../../Platform/internal/datatransfer/bridge_test.go) | A02、A03、A06 |
| FR-GW-010 | 网关须将边缘上行的设备数据、操作事件、执行反馈，按配置的对应数据模型归一化为标准数据后接入平台 | [datatransfer](../../Platform/internal/datatransfer) | [bridge_test.go](../../Platform/internal/datatransfer/bridge_test.go) | A02、A03、A06 |
| FR-GW-011 | 边缘通道须具备与边缘侧可靠传输相对等的能力 | [cloudsync](../../Platform/internal/cloudsync) | [exchange_test.go](../../Platform/internal/cloudsync/exchange_test.go) | A06、A08、A10、A14 |
| FR-GW-012 | 网关须承接云端各服务的下行内容（规则/策略/直接控制），转交边缘↔云传输模块，并将边缘回执路由回发起方 | [cloudsync](../../Platform/internal/cloudsync) | [exchange_test.go](../../Platform/internal/cloudsync/exchange_test.go) | A06、A08、A10、A14 |
| FR-GW-020 | 网关须提供自定义插件接口与开发套件（SDK），便于外部系统按统一规范接入平台 | [plugins](../../Platform/internal/plugins) | [manager_test.go](../../Platform/internal/plugins/manager_test.go) | A02、A14 |
| FR-GW-021 | 网关须预装人事系统接入插件，可获取企业组织架构、下属成员、职员等级，按配置的数据模型整理后接入平台 | [plugins](../../Platform/internal/plugins) | [manager_test.go](../../Platform/internal/plugins/manager_test.go) | A02、A14 |
| FR-GW-022 | 外部接入面以低频、批式为主，与边缘通道面在通路与背压策略上相互独立 | [plugins](../../Platform/internal/plugins) | [manager_test.go](../../Platform/internal/plugins/manager_test.go) | A02、A14 |
| FR-GW-030 | 网关须支持按「数据治理工具」中定义的数据模型（设备数据模型、联动策略模型、人力资源转化模型）对接入数据做字段映射与归一化 | [plugins](../../Platform/internal/plugins) | [manager_test.go](../../Platform/internal/plugins/manager_test.go) | A02、A14 |
| FR-GW-031 | 模型映射的字段命名须与边缘侧已冻结契约保持一致（snake_case，见 CSC-002），避免云边两侧出现命名漂移 | [datatransfer](../../Platform/internal/datatransfer) | [bridge_test.go](../../Platform/internal/datatransfer/bridge_test.go) | A02、A03、A06 |
| FR-DP-001 | 分析系统须将接入数据进行分析、汇总，抽象为资产数据、总体指标等上层概要数据 | [engine](../../Platform/internal/engine) | [engine_test.go](../../Platform/internal/engine/engine_test.go) | A11、A13 |
| FR-DP-002 | 分析系统须支持用户自定义的分析配置 | [engine](../../Platform/internal/engine) | [engine_test.go](../../Platform/internal/engine/engine_test.go) | A11、A13 |
| FR-DP-003 | 分析结果须支持一定时间的持久化保存（保留期可配置，适配时序型存储，详见 4.3） | [store](../../Platform/internal/store) | [retention_policy_test.go](../../Platform/internal/store/retention_policy_test.go) | A11、A12、A18 |
| FR-DP-004 | 分析系统须为 AI 预留受控的 MCP 接入入口，接入范围限于数据分析与告警分析（详见第 5 章） | [ai](../../Platform/internal/ai) | [ai_test.go](../../Platform/internal/ai/ai_test.go) | A17 |
| FR-DP-010 | 告警系统须根据配置自动触发告警，分发方式包括但不限于全站告警、部门告警、用户告警 | [notifications](../../Platform/internal/notifications) | [service_test.go](../../Platform/internal/notifications/service_test.go) | A13、A14 |
| FR-DP-011 | 部门告警、用户告警的分发须依据权限与身份微服务提供的组织模型解析收件范围（详见 3.6） | [notifications](../../Platform/internal/notifications) | [service_test.go](../../Platform/internal/notifications/service_test.go) | A13、A14 |
| FR-DP-012 | 通知方式包括但不限于邮件通知、短信通知 | [notifications](../../Platform/internal/notifications) | [service_test.go](../../Platform/internal/notifications/service_test.go) | A13、A14 |
| FR-DP-020 | 分析系统须与前端可视化配置页面配合，根据配置自动生成分析与告警方式，从而纳入现有分析系统 | [engine](../../Platform/internal/engine) | [engine_test.go](../../Platform/internal/engine/engine_test.go) | A11、A13 |
| FR-DP-021 | 「自动生成」须落在一个明确定义的标准对象之上 | [engine](../../Platform/internal/engine) | [engine_test.go](../../Platform/internal/engine/engine_test.go) | A11、A13 |
| FR-DP-030 | 分析与告警的配置须持久化保存 | [store](../../Platform/internal/store) | [retention_policy_test.go](../../Platform/internal/store/retention_policy_test.go) | A11、A12、A18 |
| FR-OC-010 | 须配合前端可视化编排系统，将规则按数据模型转化为标准规则对象 | [engine](../../Platform/internal/engine) | [engine_test.go](../../Platform/internal/engine/engine_test.go) | A11、A13 |
| FR-OC-011 | 标准规则对象的结构与节点类型清单是规则引擎的核心契约——可视化只是其外层表现，引擎执行的、AI 优化的、审计追溯的，归根到底都是该标准对象 | [engine](../../Platform/internal/engine) | [engine_test.go](../../Platform/internal/engine/engine_test.go) | A11、A13 |
| FR-OC-012 | 规则须支持永久持久化保存 | [engine](../../Platform/internal/engine) | [engine_test.go](../../Platform/internal/engine/engine_test.go) | A11、A13 |
| FR-OC-013 | 须为规则编排预留受控的 MCP 接入入口，范围限于规则的生成与优化，且产出为草案（详见第 5 章） | [ai](../../Platform/internal/ai) | [ai_test.go](../../Platform/internal/ai/ai_test.go) | A17 |
| FR-OC-020 | 云端控制中心只做即时执行 | [control](../../Platform/internal/control) | [control_test.go](../../Platform/internal/control/control_test.go) | A07、A09、A14 |
| FR-OC-030 | 云端可对边缘发起直接控制（引用某个预定义预案或策略 + 一个请求级标识） | [control](../../Platform/internal/control) | [control_test.go](../../Platform/internal/control/control_test.go) | A07、A09、A14 |
| FR-OC-031 | 情景研判通过则执行，并回报执行结果 | [control](../../Platform/internal/control) | [control_test.go](../../Platform/internal/control/control_test.go) | A07、A09、A14 |
| FR-OC-032 | 云端对边缘的强制执行权限动态分级，依风险等级与风险类别而定 | [control](../../Platform/internal/control) | [control_test.go](../../Platform/internal/control/control_test.go) | A07、A09、A14 |
| FR-OC-033 | 任何强制执行均须生成审计记录，快照「边缘拒绝时回报的上下文信息、风险评定，以及完整的会签/审批/现场确认链」，关联至审计微服务（见 3.4） | [control](../../Platform/internal/control) | [control_test.go](../../Platform/internal/control/control_test.go) | A07、A09、A14 |
| FR-OC-034 | 断联期间，云端的直接控制与强制路径不可用 | [control](../../Platform/internal/control) | [control_test.go](../../Platform/internal/control/control_test.go) | A07、A09、A14 |
| FR-AU-001 | 审计微服务须汇总平台与边缘控制节点的所有操作、事件，并关联到对应时刻的具体数据 | [audit.go](../../Platform/internal/store/audit.go) | [store_test.go](../../Platform/internal/store/store_test.go) | A14、A18 |
| FR-AU-002 | 数据关联须复用已冻结信封中的现成锚点 | [audit.go](../../Platform/internal/store/audit.go) | [store_test.go](../../Platform/internal/store/store_test.go) | A14、A18 |
| FR-AU-003 | 审计须横跨云端与边缘两侧，要求所有被审计服务吐出的事件遵循统一的事件信封格式 | [audit.go](../../Platform/internal/store/audit.go) | [store_test.go](../../Platform/internal/store/store_test.go) | A14、A18 |
| FR-AU-004 | 审计记录须为只追加（append-only）、且可校验（如哈希链），以满足工业安全场景下的防篡改要求 | [audit.go](../../Platform/internal/store/audit.go) | [store_test.go](../../Platform/internal/store/store_test.go) | A14、A18 |
| FR-AU-005 | 强制执行、权限变更、规则激活等高风险操作，须留存完整的责任链（操作人及其组织身份、审批/确认链），与权限身份微服务的组织模型关联（见 3.6） | [audit.go](../../Platform/internal/store/audit.go) | [store_test.go](../../Platform/internal/store/store_test.go) | A14、A18 |
| FR-AM-001 | 云端资产管理须做整个平台资产模型与层级结构的权威主库 | [assets.go](../../Platform/internal/api/assets.go) | [assets_test.go](../../Platform/internal/api/assets_test.go) | A10、A11、A14 |
| FR-AM-002 | 须厘清「资产」与「设备」的分界 | [assets.go](../../Platform/internal/api/assets.go) | [assets_test.go](../../Platform/internal/api/assets_test.go) | A10、A11、A14 |
| FR-AM-003 | 资产管理须作为标签语义的唯一权威 | [assets.go](../../Platform/internal/api/assets.go) | [assets_test.go](../../Platform/internal/api/assets_test.go) | A10、A11、A14 |
| FR-AM-004 | 资产层级的维护须同时支持自上而下与自下而上两种途径 | [assets.go](../../Platform/internal/api/assets.go) | [assets_test.go](../../Platform/internal/api/assets_test.go) | A10、A11、A14 |
| FR-AM-005 | 资产模型变更须携带实体级版本号语义（与 entity_revision 的乱序保护对齐），保证云边副本的一致性 | [assets.go](../../Platform/internal/api/assets.go) | [assets_test.go](../../Platform/internal/api/assets_test.go) | A10、A11、A14 |
| FR-IAM-001 | 须区分两层权限与身份鉴权 | [identity](../../Platform/internal/identity) | [identity_test.go](../../Platform/internal/identity/identity_test.go) | A14 |
| FR-IAM-002 | 组织模型须支持通过插件导入企业自有的组织架构，避免平台另行维护一套组织体系 | [identity](../../Platform/internal/identity) | [identity_test.go](../../Platform/internal/identity/identity_test.go) | A14 |
| FR-IAM-003 | 「谁能做什么」的权限映射须由平台自身掌握与维护，独立于导入的组织树之上——企业人事口径未必与平台的操作权限边界一一对应 | [identity](../../Platform/internal/identity) | [identity_test.go](../../Platform/internal/identity/identity_test.go) | A14 |
| FR-IAM-004 | 边缘侧权限须支持两种配置方向（对标 ThingsBoard 群组角色 Group Role） | [identity](../../Platform/internal/identity) | [identity_test.go](../../Platform/internal/identity/identity_test.go) | A14 |
| FR-IAM-005 | 组织模型的消费方至少包括 | [identity](../../Platform/internal/identity) | [identity_test.go](../../Platform/internal/identity/identity_test.go) | A14 |
| FR-CFG-001 | 配置中心须作为独立的轻量基础设施服务存在（无业务逻辑），各云端服务向其登记自描述的配置 schema（带分类、分组、模块、业务等维度作元数据） | [configcenter](../../Platform/internal/configcenter) | [subscription_test.go](../../Platform/internal/configcenter/subscription_test.go) | A10、A14 |
| FR-CFG-002 | 各服务须内嵌轻量配置客户端 | [configcenter](../../Platform/internal/configcenter) | [subscription_test.go](../../Platform/internal/configcenter/subscription_test.go) | A10、A14 |
| FR-CFG-003 | 配置变更以热更新下发为主，每次变更带版本号，受权限与身份管控，并写入审计（谁、何时、把哪一项从什么改成什么） | [configcenter](../../Platform/internal/configcenter) | [subscription_test.go](../../Platform/internal/configcenter/subscription_test.go) | A10、A14 |
| FR-CFG-004 | Web UI 配置中心页面须按分类、分组、模块与业务，统一呈现与管理所有云端平台程序的可配置项 | [configcenter](../../Platform/internal/configcenter) | [subscription_test.go](../../Platform/internal/configcenter/subscription_test.go) | A10、A14 |
| FR-CFG-005 | 敏感配置（如对外凭据）的条目纳入统一呈现，其值须存于加密的密钥库、界面打码显示，不与普通配置同等明文保存 | [configcenter](../../Platform/internal/configcenter) | [subscription_test.go](../../Platform/internal/configcenter/subscription_test.go) | A10、A14 |
| FR-CFG-006 | 配置中心只管「程序配置」——服务怎么跑的操作参数（超时、重试、连接、凭据、插件开关等） | [configcenter](../../Platform/internal/configcenter) | [subscription_test.go](../../Platform/internal/configcenter/subscription_test.go) | A10、A14 |
| FR-CFG-007 | 配置治理分三层、各有其主 | [configcenter](../../Platform/internal/configcenter) | [subscription_test.go](../../Platform/internal/configcenter/subscription_test.go) | A10、A14 |
| NFR-C-001 | 云端对边缘的强制控制权须按风险分级动态收放（见 3.3.3） | [control](../../Platform/internal/control) | [control_test.go](../../Platform/internal/control/control_test.go) | A07、A09、A14 |
| NFR-C-002 | PLC、SIS、ESD 等物理安全联锁始终独立于软件链路 | [control](../../Platform/internal/control) | [control_test.go](../../Platform/internal/control/control_test.go) | A07、A09、A14 |
| NFR-C-003 | 高风险操作（强制执行、权限变更、规则激活）须全程审计、留存责任链 | [audit.go](../../Platform/internal/store/audit.go) | [store_test.go](../../Platform/internal/store/store_test.go) | A14、A18 |
| NFR-C-004 | AI/MCP 仅获辅助性能力，执行与下发等高风险操作能力不向其暴露（见第 5 章） | [ai](../../Platform/internal/ai) | [ai_test.go](../../Platform/internal/ai/ai_test.go) | A17 |
| NFR-C-010 | 边缘控制节点须能在与云端断联期间，依据缓存配置继续自治运行 | [cloudsync](../../Platform/internal/cloudsync) | [exchange_test.go](../../Platform/internal/cloudsync/exchange_test.go) | A06、A08、A10、A14 |
| NFR-C-011 | 云边通道须支持断连恢复与续传，与边缘侧可靠传输能力对接 | [cloudsync](../../Platform/internal/cloudsync) | [exchange_test.go](../../Platform/internal/cloudsync/exchange_test.go) | A06、A08、A10、A14 |
| NFR-C-020 | 持久化按保留策略分层 | [store](../../Platform/internal/store) | [retention_policy_test.go](../../Platform/internal/store/retention_policy_test.go) | A11、A12、A18 |
| NFR-C-021 | 落库目标遵循信创合规（达梦 DDB / TDM 等），保留期与库选型在各服务需求中分别写明 | [prepare-runtime.py](../../scripts/prepare-runtime.py) | [deployment-profile-matrix.json](../../docs/evidence/deployment-profile-matrix.json) | A01、A08、A09 |
| NFR-C-030 | 各服务须吐出统一格式的事件信封，供审计横向汇总 | [audit.go](../../Platform/internal/store/audit.go) | [store_test.go](../../Platform/internal/store/store_test.go) | A14、A18 |
| NFR-C-040 | 平台落在国产软件基座之上（达梦数据库、鸿诚志远边缘网关/智能 PLC、鸿志矩阵等），数据库、传输加密、算法选型均须满足信创与国密要求 | [prepare-runtime.py](../../scripts/prepare-runtime.py) | [deployment-profile-matrix.json](../../docs/evidence/deployment-profile-matrix.json) | A01、A08、A09 |
| FR-AI-001 | MCP 接入入口只设在云端平台这一侧，且范围圈定在三件事 | [ai](../../Platform/internal/ai) | [ai_test.go](../../Platform/internal/ai/ai_test.go) | A17 |
| FR-AI-002 | 具体的执行与下发仍由人工处理 | [ai](../../Platform/internal/ai) | [ai_test.go](../../Platform/internal/ai/ai_test.go) | A17 |
| FR-AI-003 | AI 生成或优化产出的规则，须与人工手写的规则走进同一条评审、确认流水线——AI 产出的仅为草案/建议稿，须由人审阅、由人激活，AI 自身不具备让规则生效的… | [ai](../../Platform/internal/ai) | [ai_test.go](../../Platform/internal/ai/ai_test.go) | A17 |
| FR-AI-004 | AI 给出的优化须可解释、可比对，使审阅人能清楚看出改动内容与改动理由 | [ai](../../Platform/internal/ai) | [ai_test.go](../../Platform/internal/ai/ai_test.go) | A17 |

## 网关功能基线

来源：[平台数据网关需求基线.md](../../Baseline/Platform/DataGateway/平台数据网关需求基线.md)。

| 编号 | 原文内容摘要 | 实现入口 | 验证入口 | 对应验收 |
|---|---|---|---|---|
| FR-GW-101 | 平台为单租户 | [prepare-runtime.py](../../scripts/prepare-runtime.py) | [deployment-profile-matrix.json](../../docs/evidence/deployment-profile-matrix.json) | A01、A08、A09 |
| FR-GW-102 | 平台须支持两种托管形态 | [prepare-runtime.py](../../scripts/prepare-runtime.py) | [deployment-profile-matrix.json](../../docs/evidence/deployment-profile-matrix.json) | A01、A08、A09 |
| FR-GW-110 | 网关须终结边缘节点的 mTLS 连接，并以连上来的节点身份，向资产管理核验其准入状态（启用/停用） | [cloudsync](../../Platform/internal/cloudsync) | [exchange_test.go](../../Platform/internal/cloudsync/exchange_test.go) | A06、A08、A10、A14 |
| FR-GW-111 | 网关不持有节点注册表 | [cloudsync](../../Platform/internal/cloudsync) | [exchange_test.go](../../Platform/internal/cloudsync/exchange_test.go) | A06、A08、A10、A14 |
| FR-GW-112 | 网关对上行数据做校验 + 透传 | [datatransfer](../../Platform/internal/datatransfer) | [bridge_test.go](../../Platform/internal/datatransfer/bridge_test.go) | A02、A03、A06 |
| FR-GW-113 | 网关可在透传时补盖必要的接入侧标识（如接入节点、接入时刻），不改动 DeviceMessage 的既有语义字段 | [datatransfer](../../Platform/internal/datatransfer) | [bridge_test.go](../../Platform/internal/datatransfer/bridge_test.go) | A02、A03、A06 |
| FR-GW-114 | 上行投递遵循「谁接收谁负责」 | [cloudsync](../../Platform/internal/cloudsync) | [exchange_test.go](../../Platform/internal/cloudsync/exchange_test.go) | A06、A08、A10、A14 |
| FR-GW-115 | 确认语义按数据性质分两档 | [cloudsync](../../Platform/internal/cloudsync) | [exchange_test.go](../../Platform/internal/cloudsync/exchange_test.go) | A06、A08、A10、A14 |
| FR-GW-116 | 去重以 message_id 为键 | [cloudsync](../../Platform/internal/cloudsync) | [exchange_test.go](../../Platform/internal/cloudsync/exchange_test.go) | A06、A08、A10、A14 |
| FR-GW-116a | 事件档若在实现上代价过大，允许降级统一按时序处理（触发与否由设计阶段判断） | [cloudsync](../../Platform/internal/cloudsync) | [exchange_test.go](../../Platform/internal/cloudsync/exchange_test.go) | A06、A08、A10、A14 |
| FR-GW-117 | 投递失败的处置须暴露为可配置项（见 NFR-GW-03），由客户按实际需求选择 | [cloudsync](../../Platform/internal/cloudsync) | [exchange_test.go](../../Platform/internal/cloudsync/exchange_test.go) | A06、A08、A10、A14 |
| FR-GW-118 | 溢出、缓冲满时 | [cloudsync](../../Platform/internal/cloudsync) | [exchange_test.go](../../Platform/internal/cloudsync/exchange_test.go) | A06、A08、A10、A14 |
| FR-GW-120 | 下行内容为业务抽象层的规则、策略与直接控制，不含设备级协议帧（翻译由边缘命令编排引擎完成） | [control](../../Platform/internal/control) | [control_test.go](../../Platform/internal/control/control_test.go) | A07、A09、A14 |
| FR-GW-121 | 下行按紧迫度分两类处置 | [control](../../Platform/internal/control) | [control_test.go](../../Platform/internal/control/control_test.go) | A07、A09、A14 |
| FR-GW-122 | 网关按目标节点标识路由下行 | [control](../../Platform/internal/control) | [control_test.go](../../Platform/internal/control/control_test.go) | A07、A09、A14 |
| FR-GW-123 | 回执复用其对应下行请求的同一请求级标识，不为回执另设新的标识 | [control](../../Platform/internal/control) | [control_test.go](../../Platform/internal/control/control_test.go) | A07、A09、A14 |
| FR-GW-124 | 直接控制的回执（含风险评定、上下文）在回传发起方的同时，须进入审计事件流（高风险操作链证据，对齐 CSC-007） | [control](../../Platform/internal/control) | [control_test.go](../../Platform/internal/control/control_test.go) | A07、A09、A14 |
| FR-GW-127 | 下行收发以队列承载 | [configcenter](../../Platform/internal/configcenter) | [subscription_test.go](../../Platform/internal/configcenter/subscription_test.go) | A10、A14 |
| FR-GW-128 | 网关须向发起方如实回传下行的投递状态（在途 / 已达 / 超时未达 / 失败），使发起方掌握下发是否送达 | [cloudsync](../../Platform/internal/cloudsync) | [exchange_test.go](../../Platform/internal/cloudsync/exchange_test.go) | A06、A08、A10、A14 |
| FR-GW-129 | 当下行队列水位升高，须按提醒 / 警告 / 错误三档向上游（编排与控制中心）回传压力信号并施加背压，使其放缓乃至暂停下发，避免队列被撑满 | [configcenter](../../Platform/internal/configcenter) | [subscription_test.go](../../Platform/internal/configcenter/subscription_test.go) | A10、A14 |
| FR-GW-130 | 断连期间 | [cloudsync](../../Platform/internal/cloudsync) | [exchange_test.go](../../Platform/internal/cloudsync/exchange_test.go) | A06、A08、A10、A14 |
| FR-GW-140 | 网关须提供自定义插件接口与开发套件（SDK） | [plugins](../../Platform/internal/plugins) | [manager_test.go](../../Platform/internal/plugins/manager_test.go) | A02、A14 |
| FR-GW-141 | 第三方接入插件须进程级隔离运行，独立于网关主进程 | [plugins](../../Platform/internal/plugins) | [manager_test.go](../../Platform/internal/plugins/manager_test.go) | A02、A14 |
| FR-GW-142 | 本期插件方向为只接入（外部 → 平台） | [plugins](../../Platform/internal/plugins) | [manager_test.go](../../Platform/internal/plugins/manager_test.go) | A02、A14 |
| FR-GW-143 | 外部同步形态 | [plugins](../../Platform/internal/plugins) | [manager_test.go](../../Platform/internal/plugins/manager_test.go) | A02、A14 |
| FR-GW-144 | 对外凭据由网关统一管理（统一凭据库） | [configcenter](../../Platform/internal/configcenter) | [subscription_test.go](../../Platform/internal/configcenter/subscription_test.go) | A10、A14 |
| FR-GW-145 | 模型映射（外部 schema → 平台模型）须在数据治理工具中定义、版本化 | [plugins](../../Platform/internal/plugins) | [manager_test.go](../../Platform/internal/plugins/manager_test.go) | A02、A14 |
| NFR-GW-01 | 网关须支持断连恢复与续投 | [cloudsync](../../Platform/internal/cloudsync) | [exchange_test.go](../../Platform/internal/cloudsync/exchange_test.go) | A06、A08、A10、A14 |
| NFR-GW-02 | 边缘连接 mTLS | [identity](../../Platform/internal/identity) | [identity_test.go](../../Platform/internal/identity/identity_test.go) | A14 |
| NFR-GW-03 | 所有「看实际情况而定」的项（超时、重试频率、超时动作、溢出策略、拉取频率、全量/增量等）须暴露为可配置项，并接入配置中心 | [configcenter](../../Platform/internal/configcenter) | [subscription_test.go](../../Platform/internal/configcenter/subscription_test.go) | A10、A14 |
| NFR-GW-04 | 入库、传输加密、密钥库均满足信创与国密要求 | [prepare-runtime.py](../../scripts/prepare-runtime.py) | [deployment-profile-matrix.json](../../docs/evidence/deployment-profile-matrix.json) | A01、A08、A09 |
| NFR-GW-05 | 须同时适配代理云端托管与协助自托管两种形态 | [prepare-runtime.py](../../scripts/prepare-runtime.py) | [deployment-profile-matrix.json](../../docs/evidence/deployment-profile-matrix.json) | A01、A08、A09 |

## 采集功能基线

来源：[数据传递微服务需求分析文档.md](../../Baseline/数据传递微服务/数据传递微服务需求分析文档.md)。

| 编号 | 原文内容摘要 | 实现入口 | 验证入口 | 对应验收 |
|---|---|---|---|---|
| FR-S-001 | 微服务须支持通过 Connector 组件接入不同通信协议的设备 | [connector](../../DataTransfer/internal/connector) | [manager_test.go](../../DataTransfer/internal/connector/manager_test.go) | A02、A10 |
| FR-S-002 | 协议适配采用三层策略 | [connector](../../DataTransfer/internal/connector) | [manager_test.go](../../DataTransfer/internal/connector/manager_test.go) | A02、A10 |
| FR-S-003 | 每个 Connector 实例须独立运行，单个 Connector 异常不得影响其他 Connector 的正常工作 | [connector](../../DataTransfer/internal/connector) | [manager_test.go](../../DataTransfer/internal/connector/manager_test.go) | A02、A10 |
| FR-S-004 | 微服务须支持与以下四类南向通信对象交互 | [connector](../../DataTransfer/internal/connector) | [manager_test.go](../../DataTransfer/internal/connector/manager_test.go) | A02、A10 |
| FR-S-005 | 微服务须同时支持两种设备接入方式 | [connector](../../DataTransfer/internal/connector) | [manager_test.go](../../DataTransfer/internal/connector/manager_test.go) | A02、A10 |
| FR-S-006 | 自动发现的设备，数据传递微服务不得自行纳入正式采集 | [connector](../../DataTransfer/internal/connector) | [manager_test.go](../../DataTransfer/internal/connector/manager_test.go) | A02、A10 |
| FR-S-007 | 所有协议采集的原始数据须经过 Converter 组件转换为统一的 DeviceMessage 数据模型（详见第5章） | [conversion](../../DataTransfer/internal/conversion) | [precision_test.go](../../DataTransfer/internal/connector/modbus/precision_test.go) | A03 |
| FR-S-008 | Converter 须支持以下转换能力 | [conversion](../../DataTransfer/internal/conversion) | [precision_test.go](../../DataTransfer/internal/connector/modbus/precision_test.go) | A03 |
| FR-S-008a | 设备归属标签（tags）由外部的「设备注册微服务」管理并下发 | [conversion](../../DataTransfer/internal/conversion) | [precision_test.go](../../DataTransfer/internal/connector/modbus/precision_test.go) | A03 |
| FR-S-008b | 标签须支持 Connector 级默认值继承 | [conversion](../../DataTransfer/internal/conversion) | [precision_test.go](../../DataTransfer/internal/connector/modbus/precision_test.go) | A03 |
| FR-S-009 | 微服务须将上游下发的标准格式指令，通过反向 Converter 转译为目标设备的协议帧格式 | [conversion](../../DataTransfer/internal/conversion) | [precision_test.go](../../DataTransfer/internal/connector/modbus/precision_test.go) | A03 |
| FR-S-010 | 反向 Converter 须与上行 Converter 共用同一份设备配置，保证同一设备的上行和下行映射一致 | [conversion](../../DataTransfer/internal/conversion) | [precision_test.go](../../DataTransfer/internal/connector/modbus/precision_test.go) | A03 |
| FR-S-011 | 微服务须支持以下三类下行指令 | [command](../../DataTransfer/internal/command) | [persistence_test.go](../../DataTransfer/internal/command/persistence_test.go) | A05、A07 |
| FR-S-012 | 每条下发指令须具有唯一标识（commandId），微服务须追踪指令的执行状态 | [command](../../DataTransfer/internal/command) | [persistence_test.go](../../DataTransfer/internal/command/persistence_test.go) | A05、A07 |
| FR-S-013 | 指令执行状态生命周期 | [command](../../DataTransfer/internal/command) | [persistence_test.go](../../DataTransfer/internal/command/persistence_test.go) | A05、A07 |
| FR-S-014 | 微服务须支持可配置的超时时间和重试策略 | [command](../../DataTransfer/internal/command) | [persistence_test.go](../../DataTransfer/internal/command/persistence_test.go) | A05、A07 |
| FR-S-015 | 最终执行结果（成功/失败/超时）须作为 CMD_RESPONSE 类型消息上报给上游 | [command](../../DataTransfer/internal/command) | [persistence_test.go](../../DataTransfer/internal/command/persistence_test.go) | A05、A07 |
| FR-S-016 | 微服务须将以下四类消息上报给上游微服务 | [northbound](../../DataTransfer/internal/northbound) | [gaps_test.go](../../DataTransfer/internal/runtime/gaps_test.go) | A06、A08 |
| FR-S-017 | 上报须支持批量模式，允许将多条消息打包发送以提高吞吐效率 | [northbound](../../DataTransfer/internal/northbound) | [gaps_test.go](../../DataTransfer/internal/runtime/gaps_test.go) | A06、A08 |
| FR-S-018 | 方案A和方案B对上游暴露不同的接口实现（详见接口文档），但数据模型保持一致 | [northbound](../../DataTransfer/internal/northbound) | [gaps_test.go](../../DataTransfer/internal/runtime/gaps_test.go) | A06、A08 |
| FR-S-019 | 微服务须接收来自"智能命令编排与下发引擎微服务"的下行指令，并路由到正确的 Connector 执行下发 | [command](../../DataTransfer/internal/command) | [persistence_test.go](../../DataTransfer/internal/command/persistence_test.go) | A05、A07 |
| FR-S-020 | 指令路由须基于 deviceId 定位目标设备所属的 Connector 实例 | [command](../../DataTransfer/internal/command) | [persistence_test.go](../../DataTransfer/internal/command/persistence_test.go) | A05、A07 |
| FR-S-021 | 当目标设备不在线或目标 Connector 不可用时，微服务须立即返回错误状态，不做静默丢弃 | [command](../../DataTransfer/internal/command) | [persistence_test.go](../../DataTransfer/internal/command/persistence_test.go) | A05、A07 |
| FR-S-022 | 当北向网络中断时，微服务须将采集到的数据缓存至本地存储 | [buffer](../../DataTransfer/internal/buffer) | [gaps_test.go](../../DataTransfer/internal/buffer/gaps_test.go) | A06、A08 |
| FR-S-023 | 本地缓冲须支持以下策略 | [buffer](../../DataTransfer/internal/buffer) | [gaps_test.go](../../DataTransfer/internal/buffer/gaps_test.go) | A06、A08 |
| FR-S-024 | 网络恢复后，微服务须自动进入续传模式，将缓存数据按序补发，同时不影响实时数据的正常上报 | [buffer](../../DataTransfer/internal/buffer) | [gaps_test.go](../../DataTransfer/internal/buffer/gaps_test.go) | A06、A08 |
| FR-S-024a | 缓冲存储须采用两阶段提交模式，确保数据不在"写入缓冲"和"确认发送"之间丢失 | [buffer](../../DataTransfer/internal/buffer) | [gaps_test.go](../../DataTransfer/internal/buffer/gaps_test.go) | A06、A08 |
| FR-S-025 | 断连期间收到的下行指令须返回明确的错误状态（"设备不可达"），不做本地缓存排队 | [buffer](../../DataTransfer/internal/buffer) | [gaps_test.go](../../DataTransfer/internal/buffer/gaps_test.go) | A06、A08 |
| FR-S-033 | 微服务须支持可配置的上报策略，控制归一化后的数据在进入北向上报通道之前的过滤行为 | [manager.go](../../DataTransfer/internal/connector/manager.go) | [watermarks_test.go](../../DataTransfer/internal/connector/watermarks_test.go) | A04、A05、A10 |
| FR-S-034 | 上报策略须支持以下模式 | [manager.go](../../DataTransfer/internal/connector/manager.go) | [watermarks_test.go](../../DataTransfer/internal/connector/watermarks_test.go) | A04、A05、A10 |
| FR-S-035 | 上报策略须支持多级配置，低级别配置覆盖高级别 | [manager.go](../../DataTransfer/internal/connector/manager.go) | [watermarks_test.go](../../DataTransfer/internal/connector/watermarks_test.go) | A04、A05、A10 |
| FR-S-036 | 上报策略仅对 TELEMETRY 类型消息生效 | [manager.go](../../DataTransfer/internal/connector/manager.go) | [watermarks_test.go](../../DataTransfer/internal/connector/watermarks_test.go) | A04、A05、A10 |
| FR-S-037 | 当上行数据产生速率超过北向通道消费速率时，微服务须具备背压控制机制，避免内存无限增长 | [manager.go](../../DataTransfer/internal/connector/manager.go) | [watermarks_test.go](../../DataTransfer/internal/connector/watermarks_test.go) | A04、A05、A10 |
| FR-S-038 | 背压控制须支持可配置的策略 | [manager.go](../../DataTransfer/internal/connector/manager.go) | [watermarks_test.go](../../DataTransfer/internal/connector/watermarks_test.go) | A04、A05、A10 |
| FR-S-039 | 背压触发和解除须通过监控指标对外可见（见 3.9），不得静默发生 | [manager.go](../../DataTransfer/internal/connector/manager.go) | [watermarks_test.go](../../DataTransfer/internal/connector/watermarks_test.go) | A04、A05、A10 |
| FR-S-026 | 以下配置变更须支持热加载，不中断服务运行 | [configmanager](../../DataTransfer/internal/configmanager) | [persistence_test.go](../../DataTransfer/internal/configmanager/persistence_test.go) | A10 |
| FR-S-027 | 新增协议插件（第三层纯自定义）允许短暂的 Connector 重载，但不得影响其他 Connector 的运行 | [configmanager](../../DataTransfer/internal/configmanager) | [persistence_test.go](../../DataTransfer/internal/configmanager/persistence_test.go) | A10 |
| FR-S-028 | 配置来源须支持 | [configmanager](../../DataTransfer/internal/configmanager) | [persistence_test.go](../../DataTransfer/internal/configmanager/persistence_test.go) | A10 |
| FR-S-029 | 配置变更须记录变更日志，包含变更时间、变更内容、变更来源 | [configmanager](../../DataTransfer/internal/configmanager) | [persistence_test.go](../../DataTransfer/internal/configmanager/persistence_test.go) | A10 |
| FR-S-029a | 微服务须对远程配置推送具备乱序保护能力 | [configmanager](../../DataTransfer/internal/configmanager) | [persistence_test.go](../../DataTransfer/internal/configmanager/persistence_test.go) | A10 |
| FR-S-030 | 微服务须暴露以下运行指标，供外部监控系统采集 | [manager.go](../../DataTransfer/internal/connector/manager.go) | [watermarks_test.go](../../DataTransfer/internal/connector/watermarks_test.go) | A04、A05、A10 |
| FR-S-031 | 指标须以结构化格式暴露，支持外部采集系统拉取 | [manager.go](../../DataTransfer/internal/connector/manager.go) | [watermarks_test.go](../../DataTransfer/internal/connector/watermarks_test.go) | A04、A05、A10 |
| FR-S-032 | 微服务须输出结构化日志，包含时间戳、级别、模块来源、关联的 deviceId/connectorId | [manager.go](../../DataTransfer/internal/connector/manager.go) | [watermarks_test.go](../../DataTransfer/internal/connector/watermarks_test.go) | A04、A05、A10 |
| NFR-001 | 微服务须设计为可水平扩展，单实例支撑的设备规模不设硬性上限 | [capacity.go](../../DataTransfer/internal/acceptance/capacity.go) | [capacity-disk-60m.json](../../docs/evidence/capacity-disk-60m.json) | A04、A05 |
| NFR-002 | 性能基线参考（单实例，普通工控机硬件） | [capacity.go](../../DataTransfer/internal/acceptance/capacity.go) | [capacity-disk-60m.json](../../docs/evidence/capacity-disk-60m.json) | A04、A05 |
| NFR-003 | 当实例负载接近上限时，须通过监控指标给出预警信号，而非静默降级 | [manager.go](../../DataTransfer/internal/connector/manager.go) | [watermarks_test.go](../../DataTransfer/internal/connector/watermarks_test.go) | A04、A05、A10 |
| NFR-004 | 微服务须具备信创环境适配能力 | [prepare-runtime.py](../../scripts/prepare-runtime.py) | [deployment-profile-matrix.json](../../docs/evidence/deployment-profile-matrix.json) | A01、A08、A09 |
| NFR-005 | 信创合规不作为 v1.0 的硬性交付要求，但架构设计须确保可替换性，避免对特定非国产组件的硬依赖 | [prepare-runtime.py](../../scripts/prepare-runtime.py) | [deployment-profile-matrix.json](../../docs/evidence/deployment-profile-matrix.json) | A01、A08、A09 |
| NFR-006 | 方案B 下，北向通信须支持 TLS 加密传输 | [northbound](../../DataTransfer/internal/northbound) | [gaps_test.go](../../DataTransfer/internal/runtime/gaps_test.go) | A06、A08 |
| NFR-007 | 方案B 下，微服务与边缘控制节点之间须有身份认证机制（如 Token、证书双向认证） | [northbound](../../DataTransfer/internal/northbound) | [gaps_test.go](../../DataTransfer/internal/runtime/gaps_test.go) | A06、A08 |
| NFR-008 | Connector 插件（第三层纯自定义）须运行在受限环境中，不得直接访问微服务核心状态或其他 Connector 的数据 | [sidecar](../../DataTransfer/internal/connector/sidecar) | [process_test.go](../../DataTransfer/internal/connector/sidecar/process_test.go) | A02 |
| NFR-009 | 微服务异常退出后须支持自动重启，重启后自动恢复所有 Connector 连接 | [connector](../../DataTransfer/internal/connector) | [manager_test.go](../../DataTransfer/internal/connector/manager_test.go) | A02、A10 |
| NFR-010 | 单条异常数据（格式错误、转换失败）不得导致整个数据管道阻塞，须记录错误并跳过 | [conversion](../../DataTransfer/internal/conversion) | [precision_test.go](../../DataTransfer/internal/connector/modbus/precision_test.go) | A03 |
| NFR-011 | 方案B 下，本地缓冲数据须持久化存储，微服务重启后缓冲数据不丢失 | [buffer](../../DataTransfer/internal/buffer) | [gaps_test.go](../../DataTransfer/internal/buffer/gaps_test.go) | A06、A08 |
| NFR-012 | 微服务须能以单一可执行文件形式部署，减少对运行环境的依赖（不依赖特定语言运行时、不依赖大量外部动态库） | [prepare-runtime.py](../../scripts/prepare-runtime.py) | [deployment-profile-matrix.json](../../docs/evidence/deployment-profile-matrix.json) | A01、A08、A09 |
| NFR-013 | 微服务须支持通过环境变量和命令行参数覆盖配置文件中的关键设置（如北向地址、日志级别），便于容器化和自动化部署 | [configmanager](../../DataTransfer/internal/configmanager) | [persistence_test.go](../../DataTransfer/internal/configmanager/persistence_test.go) | A10 |
