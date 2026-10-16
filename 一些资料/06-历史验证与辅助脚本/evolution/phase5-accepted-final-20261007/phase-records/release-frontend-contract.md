云边发布前端契约

第五阶段使用 `/api/sf/v1` 下的类型化接口。发布对象固定程序、规则、配置的内容与依赖，节点批次使用工作负载 `identity_id`；目标中的 `node_id`、程序用途和能力由工作负载登记确定。程序工件按 SHA256 保存，页面创建发布时选择已经登记的工件及配置版本。配置公开内容只带密钥引用，凭据载荷通过节点认证的专用入口取得。

接口已完成 SQLite、PostgreSQL、HTTP 与 race 验证，类型以正式生成的 OpenAPI 与模型契约为准；隔离实际进程夹具使用 `scripts/verify-release-coordination.py --directory <新目录> --keep-running` 启动，全部验证通过后生成 `frontend-fixture.json`，其中提供云端地址和私有凭据文件路径。

| 操作 | 方法与路径 | 内容 |
| --- | --- | --- |
| 发布目录 | `GET /releases` | 当前用户可读取的固定发布对象 |
| 发布内容校验 | `POST /releases/validate` | 返回 `valid`、内容摘要、依赖次序及逐组件问题 |
| 创建发布 | `POST /releases` | 请求包含 `request_id` 与 `manifest`，同一身份的发布内容保持固定 |
| 发布详情 | `GET /releases/{id}` | 程序构建、规则正文、配置引用及内容摘要 |
| 程序工件目录 | `GET /release-artifacts` | 工件大小、SHA256、程序版本、系统架构、数据库与格式兼容范围 |
| 发布批次目录 | `GET /release-deployments` | 各次发布、当前批次、节点状态及人工处理事项 |
| 创建批次发布 | `POST /release-deployments` | 指定 `id`、`request_id`、`release_id`、`group_id`、`batches` 与理由 |
| 批次详情 | `GET /release-deployments/{id}` | 期望、准备、应用、运行摘要及实际进程实例 |
| 人工处理 | `POST /release-deployments/{id}/actions` | 按已阅版本暂停、恢复、重试失败目标或取消 |
| 退回旧发布 | `POST /release-deployments/{id}/rollback` | 明确指定旧 `release_id`、新批次身份与已阅版本；核对当前数据库及旧程序兼容范围 |
| 模板版本演进 | `POST /template-batches/{id}/evolve` | 从已有实例及绑定生成固定发布对象，返回影响设备与生成规则身份 |

批次的 `batches` 为显式工作负载身份数组，例如 `[["edge-a-runtime"],["edge-b-runtime"]]`。第一批启用后，所有目标运行报告均与期望摘要、当前代际及实际程序构建对应时，协调器才允许后一批应用。离线目标显示等待；失败目标记录组件与原因，同时停止后续批次。恢复动作采用页面最近读取的 `expected_version` 和新的 `request_id`，旧版本返回 `409`。

每个目标分别显示 `desired_sha256`、`prepared_sha256`、`applied_sha256` 和 `running_sha256`。运行信息包含代理实例、实际子进程实例、PID、程序 SHA256、程序版本、数据库迁移、规则和配置版本；节点重新启动后的报告使用新的实例和递增代际核对。旧实例、旧目标、序号倒退、相同序号的不同内容及摘要错误均返回明确错误，重复的相同报告保持幂等。

`allowed_actions` 给出当前身份可以执行的动作及条件。页面冲突时保留用户理由和选择，刷新服务端版本后让用户再次提交。发布内容校验使用 `issues[]` 展示组件身份、错误码和处理说明；旧程序无法打开当前数据库时，退回结果提供停止程序、保留当前数据、从匹配备份恢复到独立目录、补回备份后数据和再次核对运行状态的步骤。

类型化请求缺少必填字段、枚举值不合法或数组数量超出约束时返回 `422`，响应中的 `details[]` 带字段位置；业务版本冲突返回 `409`，过期登录会话返回 `401`，当前资源或动作权限不足返回 `403`。创建发布与创建批次均按当前用户及 `request_id` 核对重复内容，同一请求身份携带不同内容时返回冲突。

模板演进读取第四阶段 `template_batch` 的实例身份、设备与配置版本及绑定关系，生成规则沿用原身份；响应保存源批次、源版本、目标模板版本、生成发布摘要和影响范围。原批次可继续追溯，后续发布批次引用演进所生成的固定发布对象。

发布组件的 `target_node_ids` 指定适用节点，空数组适用于所有目标。模板生成的规则和连接器使用既有绑定的所属节点；每个目标请求、应用并报告该节点适用的完整组件集合，发布总摘要保持一致。

`last_seen_ms` 表示代理最近查询或报告时间，`last_report_ms` 表示实际进程运行信息最近通过核验的时间。`report_age_ms` 和 `report_fresh` 在读取详情时计算，默认十五秒后报告过期；页面以“最后一次报告”展示历史状态，并明确显示报告时间及是否过期。历史批次的完成结果保留，批次推进需要当前实例的有效运行报告。普通心跳和相同运行报告不会改变人工操作所用的业务 `version`。

模板演进请求包含 `id`、`request_id`、`expected_version`、`release_id`、`name`、`program_sha256` 和 `targets`；每个目标使用 `instance_id`、`template_version` 和可选 `parameters`，原批次决定设备、配置及所属节点。当前版本一实例可以演进至版本二，版本二增加控制输入与互锁的数据年龄参数，温湿度模板增加平均窗口，货物计数模板增加计数窗口。响应为 `TemplateEvolution`，包含固定 `release`、实例与绑定、影响设备、节点和规则身份；`GET /template-batches/{id}/evolutions` 查询已有演进记录。原模板批次保留其版本及输入。

详情中的 `reports` 接口最多返回最近一千份报告，完整报告仍保存在服务端。`ReleaseTarget.runtime` 保存最近实际进程信息，程序 SHA256 对应已登记工件，规则同时带固定来源摘要和应用版本，配置运行信息与真实消费者检查对应。正式部署的持久目录、原有服务选项和恢复流程见[发布部署契约](release-deployment-contract.md)。
