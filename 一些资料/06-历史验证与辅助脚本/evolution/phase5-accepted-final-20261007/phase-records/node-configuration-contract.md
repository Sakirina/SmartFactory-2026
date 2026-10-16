节点身份、配置分发与运行报告契约

本契约覆盖 E5-02 以及 E5-03 的配置准备、应用和实际运行报告，依托第四阶段已验收的 `connector_configuration`、固定 `credential_ref` 与第二阶段配置版本规则实施。公共应用装配、API 总注册、worker 总注册、迁移登记及契约导出由发布协调专家维护，节点专家维护 `internal/nodeidentity`、配置中心、订阅者、节点应用用例及第十号迁移。

认证主体是 `model.WorkloadIdentity`，字段包括 `id`、`node_id`、`program`、`purpose`、`enabled`、`capabilities`、`parameter_ids`、`connector_ids`、`generation`、`version`、`instance_id`、`instance_epoch` 和 `updated_ms`。节点、程序和用途由登记记录确定，配置范围采用明确对象 ID；`platform` 参数可由获得该参数授权的 `cloud` 或 `edge` 程序使用，其他参数要求程序匹配，参数的 `target_node_ids` 继续限制目标节点。连接器要求节点归属一致，程序为 `edge` 或 `gateway`，并具有 `connector:<protocol>` 能力。基本配置能力采用 `config.read`、`config.v2`、`config.report` 和 `credential.resolve`，发布能力采用 `release`；发布专家另外核对 `goos:*`、`goarch:*` 及发布组件声明的能力。

`nodeidentity.Service.Authenticate(ctx, token, instanceID)` 返回 `nodeidentity.Principal`，`ActivateInstance` 登记当前程序实例，cloud 的 `ValidateTx(tx, principal, capability)` 在保存其业务结果的相同事务核对当前身份修订、启用状态、凭据代次、内部凭据摘要、实例 ID、实例代次及所需能力；config 的对应入口在本地保存前通过 mTLS 在线调用 cloud，核对原始工作负载 token 与相同修订信息。`Lookup` 与 `LookupTx` 供发布协调器读取公共身份。凭据明文只存在于私有部署文件及受认证程序的请求中，数据库凭据表保存 SHA-256 摘要；撤销身份后当前实例停止取得配置和解析密钥，轮换凭据后旧凭据与旧实例同时失效，程序归属或范围变化后原认证修订失效。

内部接口采用工作负载 Bearer 凭据与 `X-SF-Instance-ID`。`POST /internal/workloads/session` 需要 `instance_id`，返回已登记身份；`GET /internal/config/v2/stream` 返回 `ConfigurationEnvelope` 事件；`POST /internal/config/v2/versions` 需要 `references` 数组，读取固定版本；`POST /internal/config/v2/credentials/resolve` 需要固定引用、`credential_ref` 和用途；`POST /internal/config/v2/reports` 保存认证主体的配置报告。正式远端通信使用 TLS 1.3 与受信任客户端证书，隔离的本机 loopback 验证允许 HTTP。配置流每秒重新核对建立时的认证修订，撤销、轮换或范围变化后发送 `identity_changed` 并结束原流，下一次连接使用当前身份；每次密钥解析再次核对当前主体、范围、用途及本次实例已取得的期望目标。SSE 写入和刷新采用三秒期限，HTTP/1 不读取响应或 HTTP/2 流量阻塞时，处理程序结束并释放订阅。

`ConfigurationReference` 的 `kind`、`id`、`version`、`digest` 为必填字段，`kind` 采用 `parameter` 或 `connector`，固定版本从 1 开始。`ConfigurationEnvelope` 保存引用、目标节点、程序、用途、动态标记、Schema、公开值或公开连接器记录及密钥引用。参数版本保存在 `parameter_version`，连接器版本保存在 `connector_configuration_version`，文档 ID 都采用 `<configuration_id>:<version>`。参数摘要由 `configcenter.ParameterDigest` 计算，连接器摘要由 `ConnectorDigest` 计算；摘要覆盖固定版本及公开配置语义，密钥载荷由固定引用和加密附加身份绑定。连接器复用第四阶段的 `<connector_configuration_id>:<version>` 密钥记录，并核对解密载荷的对象身份、版本以及移除密钥后的公开内容。

`GetReleaseConfigurationTx` 和其非事务入口只执行固定版本、摘要和当前身份校验，读取保持当前期望。cloud 发布协调器在自身事务中声明 assignment，随后代理向 config 的 `POST /internal/config/v2/release-target` 提交 `deployment_id`、全局递增 `generation` 和该节点的全部固定配置引用，config 在线调用 cloud 的 `POST /internal/authority/release-target` 核对当前主体、活动批次和完整引用，再通过本地代际 CAS 固定目标；已经建立的订阅遵守固定目标，下一次明确发布或兼容退回再更新目标。这个集合表示该工作负载的完整期望，同一事务删除集合之外已发放的 `configuration_target`，空集合保持明确固定状态，旧引用的密钥解析和运行报告随后失去目标授权。其他工作负载的目标分别保存。普通订阅在没有发布目标时跟随已授权对象的最新版本；`purpose=release-runtime` 在尚无固定目标时拒绝最新版本流。发布子进程从私有固定 payload 启动，应用启动入口为 `Application.ApplyNodeReleaseConfiguration`，统一发布运行组件先调用 `PinLocalRelease` 再调用 `Subscriber.ApplyEnvelopes(..., startup=true)`，运行期间由受认证代理报告其实际进程信息。

连接器正式来源从所属 Edge 的 `Business.SaveConnectorConfiguration` 开始：原 Edge 数据库、主密钥和密钥审计继续保存固定版本，mTLS 云边同步将公开 `connector_configuration_version` 送至 cloud。config 的 `EnsureConnectorVersions` 通过受信 `config-service` 证书向 cloud 的 `/internal/authority/connector-metadata` 读取公开版本和节点登记，将公开摘要、节点修订、来源 HTTPS URL 及证书摘要保存为固定登记；这些地址来自经认证的节点登记。配置中心解析固定密钥时，转发原工作负载 token、实例、用途及当前发布证明，校验所属 Edge 的证书摘要并调用 `/internal/config/v2/connector-credentials/resolve`，所属 Edge 在线核对 cloud 当前身份及适用发布目标，解密后核对固定载荷的对象、版本和公开内容，配置中心在保存审计前再次核对当前目标。公开元数据不包含密钥值。

`ConfigurationReport` 的认证主体、节点、程序、用途、实例、实例代次、凭据代次、序号、配置种类、对象 ID、`desired`、`prepared`、`applied`、`running`、状态与时间字段均有显式必填声明。版本状态中的 `version=0` 表示该阶段尚无已确认版本，`digest` 同时为空；正版本带有对应摘要。`reason` 与公开 `effective_value` 为可选字段，密钥配置省略实际密钥值。状态采用 `prepared`、`running`、`failed`、`restart_required`；相同实例的每个对象序号递增，完全相同的重复报告按原报告返回，过期序号、相同版本不同摘要、请求体伪造主体及旧实例报告分别拒绝。服务端时间覆盖请求时间，公开失败原因采用有界错误代码。

动态应用先保存准备信息，再调用实际消费者；成功后通过运行时读取确认有效值，失败后恢复先前参数和运行策略并保留旧应用、运行版本。静态参数在运行期间保存准备版本并报告 `restart_required`，实际运行仍引用先前版本；新进程启动后应用准备的固定值，再报告新的实际运行版本。`RuntimeSnapshot` 从运行策略和消费者保存的进程状态读取有效值，报告与实际运行检查共同形成验收依据。

连接器运行检查通过生成的 `GetConnectorConfiguration` gRPC 读取 DataTransfer `ConfigManager` 的实际内容、应用代次和应用身份。固定来源版本继续记录在 `ConfigurationReference`，本地应用代次在消费者实际修订与已保存修订的最大值之后递增；明确退回旧来源也使用新的应用代次。消费者把应用身份与内容放在同一次持久提交中，桥在初次应用后同时核对应用身份、正规化完整载荷匹配和公开摘要，因此仅密钥载荷改变也有可检查的采用身份。消费者成功、平台记录失败后，下一次应用先读取实际修订，再分配新代次；后续运行快照发现修订、身份或公开摘要不同会返回失败。普通 Business 保存保留固定版本和队列任务，当前发布固定记录阻止这些任务改变消费者，第四阶段的旧任务忽略语义继续适用。

下一发布移除上一发布管理的配置时，`PinLocalRelease` 先核对其固定来源并删除对应实际连接器消费者，再清除仍匹配上一固定来源的运行态、准备态及参数并重新应用运行策略；独立普通配置保留。发布管理的程序必须使用所属 Edge 的数据库、主密钥及 DataTransfer 地址，使普通保存任务、固定发布和实际运行查询使用相同状态。

管理接口与生成客户端的约定如下：

| 方法与路径 | 请求或返回 | 权限与省略语义 |
|---|---|---|
| `GET /api/sf/v1/workload-identities` | `WorkloadIdentity[]` | 当前人工身份具有节点读取权限，只返回可读取节点的公共身份 |
| `POST /api/sf/v1/workload-identities` | `SaveWorkloadIdentityInput` 与 `WorkloadIdentity` | `identity`、`expected_version` 必填，人工身份具有新旧节点的 `identity` 权限；新建使用合法零值 0 |
| `POST /api/sf/v1/workload-identities/{id}/rotate` | `RotateWorkloadCredentialInput` 与 `WorkloadIdentity` | `credential` 和 `expected_version` 必填，凭据长度 32 至 512，当前人工身份具有节点 `identity` 权限，响应保留代次 |
| `GET /api/sf/v1/configuration-reports` | `ConfigurationReport[]` | 当前人工身份具有相应节点读取权限，报告保留准备、应用和运行版本，密钥有效值省略 |
| `GET /api/sf/v1/configuration-runtime` | `RuntimeConfiguration[]` | 当前人工身份具有本节点读取权限，参数读取实际运行策略，连接器包含固定来源、消费者公开摘要和应用代次 |

创建身份的请求必填 `id`、`node_id`、`program`、`purpose`、`enabled`、`capabilities`、`parameter_ids` 和 `connector_ids`，服务端产生的 `generation`、`version`、`instance_id`、`instance_epoch`、`updated_ms` 标记为只读；合法 `enabled=false`、空授权数组和创建用 `expected_version=0` 具有明确含义。轮换要求已有版本从 1 开始，凭据标记为只写。独立 JSON Schema 与 OpenAPI 同步必填字段、配置种类和报告状态枚举、版本最小值及只读、只写注解；内部请求解码递归核对数组中的固定引用和嵌套版本字段，缺少这些字段返回 HTTP 400，人工管理请求缺少必填字段返回 HTTP 422。身份管理在 cloud 权威数据库执行，config 的对应写入口拒绝修改远端权威身份。cloud 的报告查询透传原用户 token 至 config，config 在线取得当前人工身份并逐节点过滤报告。

正式部署准备生成私有 `workloads.json` 和各程序独立凭据文件，cloud 通过 `SF_WORKLOAD_BOOTSTRAP_FILE` 仅在首次安装登记权威身份。config 保留原有独立配置数据库、节点 ID、主密钥与密钥记录，使用 `SF_CONFIG_AUTHORITY_URL` 在线调用 cloud 权威服务，部署采用 `config-service` 客户端证书；cloud 访问 config 的有限公开元数据使用专用 `config-client-cloud-1` 证书。既有身份再次启动时保留管理操作形成的范围、轮换与停用状态。程序使用 `SF_CONFIG_TOKEN_FILE`，配置中心监听和程序访问都使用 `SF_CONFIG_TLS_CA`、`SF_CONFIG_TLS_CERT`、`SF_CONFIG_TLS_KEY`，生成 URL 为 `https://config:8092`。旧共享令牌订阅仅在显式 `SF_CONFIG_LEGACY_SUBSCRIPTION=true` 时启用，并作为迁移期间的受控兼容方式；完成旧程序凭据迁移、身份范围与实际运行检查后关闭该选项，正式生成部署采用关闭状态。

专家验证将使用两个独立程序进程及独立工作负载身份，覆盖正在连接的订阅、范围变化、主体伪造、停用、凭据轮换、固定密钥授权、动态成功与失败、静态等待及重新启动后的实际值，并核对固定发布目标与并行版本读取。原始命令、首轮失败、补充验证及受测源码和二进制摘要索引保存在 `.local/evolution/node-configuration-verified.json`，专项完成后补充最终证据关联。

cloud 权威保存用户、会话、授权、资源、工作负载身份和发布协调状态，config 继续承担独立程序配置基础设施。`POST /internal/authority/workload` 接受 authenticate、activate 或 validate，核对原 token 和当前身份；`POST /internal/authority/user` 核对原用户 token、操作与资源，config 的管理写入、分发、密钥解析及报告保存都在对应授权点在线核验。权威服务调用采用三秒期限，超时或不可用返回 HTTP 503，配置流报告 `authority_wait` 并保留已经取得的本地运行配置；程序可以从已验证缓存启动，后台持续尝试登记新实例和订阅，首次无缓存时保持安装默认配置等待权威配置。恢复后重新核对当前主体，再处理后续目标和报告；恢复验证同时核对新进程的实例 ID、实例代次、报告时间和消费者有效值。cloud 持写事务期间不调用需要回调 cloud 的 config 入口，避免独立 SQLite 服务等待自身事务。

前端夹具通过 `python3 scripts/verify-node-configuration.py --directory /private/tmp/<new-directory> --frontend` 生成，实际地址记录在该目录的 `frontend.json`，人工登录凭据只保存在 `private/frontend-credentials.json`（0600），工作负载凭据、数据库及证书位于同一私有目录。`private/running-processes.json` 保存本轮进程名称与 PID；使用夹具完成后按登记进程逐一发送 SIGTERM，并核对命令行仍属于该夹具目录。最终有效夹具路径和二进制摘要以专家验证索引为准，先前夹具保留为历史证据。
