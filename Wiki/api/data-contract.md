# 数据、版本与消息契约

[Wiki 首页](../README.md) · [HTTP API](rest.md)

公共契约目录保持 `1.0`，应用版本由各部署包记录；字段采用 snake_case，时间使用 Unix 毫秒。观测、查询、动作及发布对象保存各自身份和版本，客户端按所属字段处理续页、冲突与恢复。

## 观测结构

| 字段 | 内容 |
| --- | --- |
| `id`、`message_id` | 观测和原消息身份 |
| `source_id`、`source_sequence` | 来源及递增序号 |
| `device_id`、`key`、`value`、`unit` | 设备、测点、JSON 原值与单位 |
| `observed_ms`、`received_ms`、`time_source` | 观测时刻、业务取得时刻及时间来源 |
| `quality`、`quality_reason` | `GOOD`／`BAD`／`UNCERTAIN` 与质量原因 |
| `entity_revision`、`asset_version` | 设备配置与资产历史版本 |
| `definition_id`、`rule_version` | 派生结果的正式规则来源 |
| `late`、`revision` | 迟到标记与结果修订 |

必填、可空和结构以 [Observation Schema](../../contracts/1.0/Observation.schema.json) 为准。DataTransfer 使用 Protobuf 的整数、浮点、布尔、文本和二进制分支，二进制进入业务 JSON 后采用 Base64 文本。

## 查询及订阅结构

`QueryPage` 保存 `items`、规范化 `scope`、快照游标、续页令牌和 `metadata`，每个 `QueryRow` 保存对象身份、种类、修订、版本、排序时间和数据。趋势元数据提供质量、来源、修订与缺口，`quality_scope` 描述统计采用当前页或相应范围。

`QueryEvent` 携带 snapshot、delta、checkpoint 或 reset，事件游标采用不透明字符串。数据提交游标、对象业务版本、分页令牌各自对应其所属机制，接口示例及恢复次序见[HTTP API](rest.md)。

兼容 `DataResult` 使用 `points`、`latest`、`quality`、`sources`、`revisions`、`gaps`、`data_version`、`truncated`，显式请求 `include_latest=true` 时提供最新值。`quality.missing` 可为空，完整程度为 `complete`、`incomplete` 或 `unknown`。

## 精确值和版本

JSON 值可以是大整数、浮点、字符串、布尔和复合结果。客户端在解析阶段保存原数字表示，前端精确解析器与完整导出沿用该表示；图表的显示坐标使用绘制数值。计算内核采用精确整数或有理数处理相应运算，原生 ThingsBoard 投影遇到超出有符号整数范围的值时保存文本，业务存储继续保存原值。

草稿修订固定某次编辑，正式规则版本固定内容与生效时刻，本地采用版本记录节点应用过程。参数／连接器配置版本、工作负载凭据代次及发布部署代次均随各自结构传递，固定配置再次采用时继续保留来源版本与新的本地应用记录。

## 消息和动作身份

观测事件和回执重投沿用原 `message_id` 及内容，持久提交后完成确认，同一身份的不同内容返回冲突。计数采用稳定事件身份，同一时间的不同事件分别计算。

控制的 `downlink_id` 关联整次请求，`command_id` 关联设备步骤；人工核对、恢复与取消继续保留原执行及独立操作身份。结果未知时按[控制指南](../user-guide/control.md)读取原命令证据，业务工单与交接使用已阅对象和动作依据。

本页依据：[公开模型](../../Platform/pkg/model)、[查询结构](../../contracts/1.0/QueryPage.schema.json)、[订阅结构](../../contracts/1.0/QueryEvent.schema.json)、[采集 Protobuf](../../DataTransfer/proto/datatransfer/v1/datatransfer.proto)、[业务桥](../../Platform/internal/datatransfer/bridge.go)、[精确 JSON](../../Frontends/src/precision.ts)。
