# 业务流程前端契约

本契约对应第四阶段 E4-04、E4-05、E4-06、E4-08、E4-09，正式 HTTP 根路径为 `/api/sf/v1`。模型和路由由 `Platform/cmd/sf-contracts` 生成；前端从最终组合生成的 OpenAPI、JSON Schema 和 TypeScript 类型取得字段，本文说明页面需要表达的业务含义及实际验证方式。

所有业务读取和变更都经过应用用例的当前身份、会话、授权、资源及对象修订校验。HTTP 页面与后续 AI 工具共用同一应用入口。涉及精确整数和小数的参数继续使用现有精确 JSON 处理方式，例如 `9007199254740993` 在比较结果中保留原数值。

## 页面入口与响应

| 操作 | 方法与路径 | 响应及用途 |
| --- | --- | --- |
| 协议目录 | `GET /device-protocols` | Modbus TCP、MQTT、OPC-UA 的字段类型、必填、单位、范围、枚举、帮助及扩展保存规则 |
| 配置预检 | `POST /device-configurations/validate` | `valid`、`issues[]`、规范化 `parameters` 与正式 `DeviceConfigUpdate` JSON |
| 设备配置 | `GET /devices/{id}/configuration` | 当前实体、设备参数、允许保存的状态 |
| 保存设备配置 | `POST /device-configurations` | 归属边缘节点检查版本后保存并进入现有 DataTransfer 配置队列 |
| 连接器配置目录 | `GET /connector-configurations` | 当前授权范围内的正式配置、应用回执及允许操作 |
| 保存连接器配置 | `POST /connector-configurations` | 归属边缘节点登记正式版本并下发；云端读取同步副本 |
| 告警详情 | `GET /alarms/{id}` | 规则生命周期、人工处置、原生状态、同步状态及允许操作 |
| 告警处置 | `POST /alarms/{id}/actions` | 确认、责任分配、备注、完成或再次处理 |
| 工单目录与详情 | `GET /work-orders`、`GET /work-orders/{id}` | 当前可读工单；详情含处理记录、交接记录和责任异常 |
| 创建工单 | `POST /work-orders` | 原子保存工单、创建记录及审计 |
| 工单变更 | `POST /work-orders/{id}/actions` | 分配、开始、修改、备注、完成或再次处理 |
| 工单交接 | `POST /work-orders/{id}/handovers` | 原子保存责任人变更、未完成事项、已阅证据及审计 |
| 语义差异 | `POST /drafts/{id}/semantic-diff` | 已阅草稿与指定发布版本的身份、修订、差异位置、类型和单位 |
| 影响分析 | `POST /drafts/{id}/impact` | 依赖及被依赖规则、设备、输出、仪表盘和工单使用位置，以及不完整原因 |
| 场景模板 | `GET /scene-templates` | 五类固定版本模板、协议、数据字段、动作能力、参数及草稿数量 |
| 创建模板批次 | `POST /template-batches` | 完整草稿或失败记录；一个批次在同一事务提交 |
| 批次详情与重试 | `GET /template-batches/{id}`、`POST /template-batches/{id}/retry` | 保存的输入、绑定版本、尝试历史与恢复后的结果 |

`request_id` 在同一用户、对象及用例范围内标识一次操作，相同内容再次提交会取得已保存结果；同一标识改换内容返回 `409`。页面收到版本冲突后重新读取详情，保留用户填写的理由，并让用户根据更新后的对象再次提交。身份失效返回 `401`，当前授权拒绝返回 `403`，对象不存在返回 `404`；参数和状态错误由统一错误响应给出说明。模板实例校验失败保存为 HTTP 成功响应中的 `status: "failed"`，页面通过 `failures[]` 展示具体实例和原因。

## 设备与连接器参数

协议目录的 `sections` 使用 `connection`、`connection.tls`、`polling`、`address`、`datapoints`、`report_strategy`、`converter.action_mappings` 等路径；`repeated` 表示数据点列表，`keyed` 表示以动作名称索引的对象。服务端使用同一目录验证基础字段，并检查寄存器宽度及地址范围、OPC-UA 安全模式、TLS 证书与私钥配对、上报周期和动作映射。

已有配置中的扩展对象按原 JSON 保存，编辑数据点时按 `key` 保留未改动扩展。顶层正式载荷继续采用 Protobuf JSON，`connection` 等内部字节字段由服务端解码为可编辑对象。TLS 同时接受历史大写 Go 字段名与当前蛇形字段名，保存时输出蛇形字段；两种名称给出不同内容时返回校验失败。

设备配置保存需要 `id`、`request_id`、`expected_version` 和 `parameters`。连接器配置保存需要 `request_id`、`expected_version`、`group_id`、`edge_id`、`protocol` 和 `parameters`；首次登记的 `expected_version` 为零。配置身份为 `edge_id/connector_id`，版本、协议及真实动作映射共同参与模板绑定检查。

连接器密码加密保存在归属节点，公开配置仅包含不透明 `credential_ref`；每个配置版本使用独立密钥引用。保存新版本后，已进入处理过程的旧任务继续使用原版本的载荷，DataTransfer 的配置修订校验保护较新有效配置。公开配置和 `connector_configuration_receipt` 随云边同步传播，密钥载荷保留在归属节点。`receipt.configuration_version` 对应实际确认版本；`pending`、`applied`、`failed` 随响应给出，页面据此展示下发结果。

## 告警与原生状态

`alarm.active` 表示规则条件当前仍在触发，`alarm.cleared_ms` 记录规则恢复。`case` 保存人工确认、责任人、处理状态及不可变操作记录；`native` 保存 ThingsBoard 的确认、清除和来源版本。原生平台清除一条仍满足规则条件的告警时，详情同时呈现原生已清除与规则仍在触发，用户能够分别看到两条状态事实。

每次人工动作提交 `expected_version = detail.case.version` 与 `expected_action_version = detail.action_version`。动作版本包含告警身份、定义版本、级别、触发或恢复、确认及历史修订状态；连续观测引起的计数、最新值及更新时间变化允许继续使用同一次已阅详情。页面操作按钮采用 `allowed_actions` 的结果及说明，提交时应用仍会再次校验。

```json
{
  "request_id": "alarm-review-001",
  "expected_version": 2,
  "expected_action_version": "详情返回的不透明版本",
  "action": "note",
  "reason": "现场已检查传感器，继续观察温度变化"
}
```

`action` 支持 `acknowledge`、`assign`、`note`、`complete`、`reopen`，分配时提供 `assignee_id`。操作记录包含操作者、理由、来源节点、来源序号、已阅版本、时间及因果依据。云边同时分配不同责任人时，`conflict_fields` 显示冲突；缺少前序操作时，`pending_basis` 显示等待同步的依据，后续完整决策通过相同动作流程解决冲突。

原生适配器启动后立即读取首轮，此后正常每五秒读取一次，每轮最多 64 个映射、执行期限十秒；失败触发现有后台退避，最长三十秒。单条映射失效会保留本地失败记录并推进游标，后续告警继续同步，下一次轮转重新尝试失败项。`native_sync` 返回启用状态、最后尝试与成功时间、失败映射数、批次上限、期限及状态；内部游标不向详情暴露。受信回调 `POST /internal/native/alarms/{id}` 使用服务凭据，并再次校验绑定的原生身份、启动时间和来源版本。

告警查询使用查询专家提供的 `POST /queries/alarms` 及对应订阅。人工处置保存在查询行 `data.handling_case`，原生状态保存在 `data.native`，两类变化都会推进查询行 `revision`。

## 工单与交接

工单状态为 `open`、`in_progress`、`completed`，动作包括 `assign`、`start`、`update`、`note`、`complete`、`reopen`。处理操作使用详情中的工单 `version`，分配校验目标成员当前启用状态、部门和全部关联资源权限；关联执行同时检查已发布规则版本、当前规则、输入快照及命令证据涉及的设备。

详情中的 `responsibility_issue` 说明责任人停用、部门变化或权限变化造成的处理阻碍，`allowed_actions` 返回当前可以执行的重新分配或后续动作。交接需要当前责任人或管理员操作，并保存交出、承接的成员和部门、未完成事项以及用户选择的已阅证据。

```json
{
  "request_id": "handover-001",
  "expected_version": 3,
  "from_user_id": "operator-a",
  "to_user_id": "operator-b",
  "pending_items": ["复核下一班次的温度曲线"],
  "evidence": [{
    "kind": "alarm",
    "id": "当前工单关联的告警身份",
    "version": 71,
    "action_version": "已阅告警详情返回的不透明版本",
    "description": "交班时已核对告警和现场情况"
  }],
  "reason": "交给下一班次继续检查"
}
```

告警证据同时保存已阅观测版本和动作版本；提供 `action_version` 后，新增采样计数允许交接继续进行，级别、恢复或确认等动作含义变化会产生冲突。执行及工单证据使用对应对象版本。每次交接的责任变更、交接记录、请求身份和审计在同一事务提交。

## 语义差异与影响分析

语义比较请求包含 `expected_version` 和 `published_version`；发布版本为零时采用草稿的 `base_version`。比较按节点身份、连接及端口、参数、选择范围、输出、控制政策和引用展开，忽略节点位置以及具有相同领域含义的集合排列。重复连接保留数量，空值与缺失字段分别报告，精确数值按数值含义比较。

结果的 `draft`、`published` 包含对象身份、修订及内容摘要，页面编辑草稿后用这些字段识别旧结果。`changes[]` 给出类别、位置、增加或删除或修改、前后类型、数值和可用单位；参数单位来自正式节点目录。

影响分析请求包含 `expected_version` 和 `budget`，默认预算 512，范围为 1 至 2000；`complete`、`issues`、`visited` 和 `budget` 随结果返回。循环、缺失引用、受限引用和预算不足会使结果标为不完整，页面保留这些原因。引用结果只展示当前用户可访问的对象，经过受限中间规则的路径省略该规则身份。

## 五类模板与批次

| 模板身份 | 协议 | 生成草稿 |
| --- | --- | --- |
| `climate-ventilation` | `modbus_tcp` | 温湿度与通风，14 份 |
| `infrared-lighting` | `mqtt_device` | 红外照明，2 份 |
| `hazardous-gas` | `opcua` | 危气监测，6 份 |
| `agv-obstacle` | `modbus_tcp` | AGV 避障，2 份 |
| `goods-counting` | `mqtt_device` | 货物计数，1 份 |

每个实例必须提供 `template_id`、`template_version`、`id`、`name`、`device_id`、`device_version`、`configuration_id` 和 `configuration_version`；省略 `parameters` 时使用模板默认值，包含控制策略的模板还需要当前安全成员 `safety_user_id`。页面从当前设备和正式连接器配置读取已阅版本，连接器动作能力取自实际 `converter.action_mappings`。

一个批次包含 1 至 20 个实例，在云端准备；全部实例通过组织归属、协议、能力、参数、权限、名称及身份检查后，所有草稿与审计一起保存。失败批次保存原输入、失败实例与尝试记录，并保持草稿集合为空；已经使用的历史对象身份继续参与冲突检查。外部问题修复后，使用批次版本和新 `request_id` 调用重试，成功重试再次提交相同请求仍返回同一批结果。修改实例参数或绑定版本时创建新批次身份，以便保留此前的失败记录。

准备成功的草稿继续调用统一草稿校验及发布用例；连接器 `pending` 状态会保留在批次绑定快照中，供页面展示。第五阶段复用该配置身份、版本和回执完成批量发布前检查。

## 隔离前端夹具与验证

`Platform/cmd/sf-business-fixture` 使用新目录创建独立 SQLite 数据库、随机开发凭据和五类设备配置，并通过实际规则引擎每秒产生一次温度观测。云端模式包含 25 份模板草稿；边缘模式允许编辑归属设备及连接器配置。每次运行使用新目录，`manifest.json` 保存对象身份，`samples.json` 保存协议、模板、告警和工单样例，登录凭据单独写入权限为 `0600` 的 `private-credentials.json`。

1. 在 `Platform` 目录构建 `go build -o /tmp/sf-business-fixture ./cmd/sf-business-fixture`
2. 启动云端示例 `/tmp/sf-business-fixture -dir /tmp/sf-business-cloud-new -mode cloud -address 127.0.0.1:19310`，需要边缘表单时用另一个新目录及端口启动 `-mode edge`
3. 从该目录的凭据文件读取 `operator-a` 登录信息；`operator-b` 使用同一随机密码，供交接流程切换成员
4. 将前端 API 代理指向对应地址，或通过 `-static` 指定已经构建的前端目录；读取 `manifest.json` 取得具体告警、草稿和工单身份
5. 完成页面检查后发送 `SIGINT`，或使用 `-duration 90s` 自动停止夹具

此夹具用于单实例页面检查，实际云边双向处置通过独立 mTLS 测试，原生状态通过隔离 CE 实例验证。验证报告保存第一次失败、修复后的命令、源码及编译输入清单、测试二进制摘要和资源清理结果，汇总位置为 `.local/evolution/business-workflows-verified.json`。
