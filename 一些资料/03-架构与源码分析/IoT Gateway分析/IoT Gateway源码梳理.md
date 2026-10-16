**整体定位**

这个 IoT Gateway 的职责很集中：它运行在靠近设备、协议服务、数据库或文件源的一侧，把各种异构协议里的数据转换成 ThingsBoard 能理解的统一格式，再通过 MQTT Gateway API 发往 ThingsBoard 平台。同时，它也接平台下发的 RPC、属性更新、远程配置，把这些控制动作重新路由回对应的连接器或本地管理模块。

所以它的核心可以理解成五层：入口与配置层、连接器层、统一数据层、本地缓冲与发送层、平台管理与下行控制层。

**主编排器**

整个运行时的中心是 `TBGatewayService`，源码在 [tb_gateway_service.py](/path/to/competition-workspace/upstream/thingsboard-gateway/thingsboard_gateway/gateway/tb_gateway_service.py)。它启动后会读取 `tb_gateway.json`、`logs.json` 和各 connector 配置文件，然后创建存储、报送策略、设备过滤器、ThingsBoard 客户端、连接器实例、远程配置器，以及一组后台线程。

图里的大框 “TBGatewayService - main orchestrator” 就对应这个类。它维护设备注册表、连接器索引、转换数据队列、RPC 队列、属性同步队列、已发布事件记录，以及 connector reload、watcher、统计、远程日志等辅助能力。换句话说，Gateway 的业务状态几乎都由它协调。

**连接器层**

连接器层负责接触外部世界。代码里的默认连接器来自 `DEFAULT_CONNECTORS`，实际是 15 类：MQTT、Modbus、OPC UA、BLE、Request、CAN、BACnet、ODBC、REST、SNMP、FTP、Socket、XMPP、OCPP、KNX。这里我按源码修正了 Claude 页面里容易引起误会的连接器数量。

每个连接器都遵守 `Connector` 抽象接口：`open()`、`close()`、`is_connected()`、`on_attributes_update()`、`server_side_rpc_handler()` 等。它们各自管理协议连接、重连、轮询、订阅、请求、解析，然后把协议原始数据交给 converter。

连接器的关键点是：协议差异都限制在 connector 与 converter 内部。到了 Gateway 中央层，数据会被转换成统一的 `ConvertedData`。

**统一数据模型**

`ConvertedData` 是上行数据的标准形态，包含 `deviceName`、`deviceType`、`telemetry`、`attributes`、`metadata`。它还支持按 `maxPayloadSizeBytes` 拆分，避免单次 MQTT payload 超过配置限制。

这一步很重要，因为后面的存储、过滤、报送策略、MQTT 发布逻辑，都依赖这个统一结构。也就是说，新增一个协议时，编码人员主要要实现 connector 生命周期、converter 转换规则，以及下行 RPC/属性写回逻辑；中间的数据管线可以复用。

**上行数据路径**

数据流动图 A 区展示的是上行主链路：设备或外部源先进入 connector，connector 解析协议数据并调用 converter，converter 输出 `ConvertedData`，然后 connector 调用 `gateway.send_to_storage()`。

进入 `send_to_storage()` 后，Gateway 会先经过可选的 `DeviceFilter`，再处理延迟元数据和报送策略。报送策略支持 `ON_RECEIVED`、`ON_CHANGE`、`ON_REPORT_PERIOD`、`ON_CHANGE_OR_REPORT_PERIOD`、`DISABLED` 等模式，可以按全局、connector、数据点几个层级决定数据是否需要进入后续队列。

通过策略的数据进入 `__converted_data_queue`。后台的 storage fill thread 会批量取出队列内容，触发设备生命周期副作用，比如 `add_device()`、`gw_connect_device()`、发送 connectorName 与 connectorType 属性，然后把 JSON 事件包写入本地 `EventStorage`。

**本地存储与可靠发送**

Gateway 有三种事件存储实现：内存、文件、SQLite。存储层的接口是 `put()`、`get_event_pack()`、`event_pack_processing_done()`。它承担的是“平台连接暂时不可用时，事件可以先留在本地，之后继续发送”的职责。

发送线程 `__read_data_from_storage()` 会在 ThingsBoard 客户端已连接时读取事件包，按 `deviceName` 聚合 telemetry 与 attributes，然后调用 `__send_data()`。如果发布 future 全部成功，才调用 `event_pack_processing_done()` 确认这一批事件已经处理完成；如果发布失败，事件会留在存储里，等待之后再次发送。

这就是图里从 EventStorage 到 Send data thread，再到 Publish、ThingsBoard 的那条红蓝混合路径。

**平台连接层**

平台连接由 `TBClient` 包装 `TBGatewayMqttClient` 完成，源码在 [tb_client.py](/path/to/competition-workspace/upstream/thingsboard-gateway/thingsboard_gateway/gateway/tb_client.py)。它支持 access token、用户名密码、X.509、provisioning、TLS、proxy、rate limits、重连等。

上行发送分两类：Gateway 自身属性/遥测用 `send_attributes()`、`send_telemetry()`；子设备数据用 `gw_send_attributes()`、`gw_send_telemetry()`。这也是 ThingsBoard Gateway API 的核心边界：平台看到的是一个 Gateway 设备，以及它代管的多个 child devices。

**下行 RPC 与属性**

数据流动图 B 区是平台到边缘侧的下行路径。平台下发 RPC 或属性后，MQTT 客户端的订阅回调会进入 `_rpc_request_handler()` 或 `_attribute_update_callback()`。

RPC 分两类：如果目标是具体子设备，Gateway 会把请求写入 `__rpc_to_devices_queue`，再根据设备名找到所属 connector，调用 connector 的 `server_side_rpc_handler()`；如果目标是 Gateway 本身，则由 gateway RPC 方法处理，比如 ping、stats、devices、update、version、device_renamed、device_deleted 等。

属性更新也类似。Gateway 会判断这是 Gateway 属性还是子设备属性，然后路由到对应 connector 的 `on_attributes_update()`。connector 再把属性变化翻译成协议写入、请求、命令或本地状态变化。

**远程配置与 watcher**

数据流动图 C 区讲的是管理面。`RemoteConfigurator` 会把当前配置作为 Gateway attributes 发到平台，也会监听平台发来的配置属性，例如 general、storage、grpc、logs、active_connectors、connector config 等。

当配置更新进入 `RECEIVED_UPDATE_QUEUE` 后，远程配置线程会按类型执行处理：重建 TBClient、替换 EventStorage、初始化或关闭 gRPC、更新日志、重载 connector、更新设备过滤或报送策略。与此同时，`_watchers()` 会处理重连后的重新订阅、connector 配置文件变化检测、RPC 超时、共享属性请求、日志版本更新等周期性任务。

这部分让 Gateway 可以通过平台进行运行时维护，但它仍然以本地配置文件和源码能力为边界；图里没有纳入 ThingsBoard 服务器内部实现。

**可选 gRPC 连接器路径**

D 区是可选的外部 connector 进程路径。Gateway 内部启动 `TBGRPCServerManager`，外部进程基于 `GwGrpcConnector` 连接到本地 gRPC 服务，注册 connector id，然后通过 protobuf 消息上送 telemetry、attributes、connect、disconnect、RPC response 等。

Gateway 侧用 `GrpcUplinkConverter` 把这些消息转成核心 API 调用，例如 `send_to_storage()`、`add_device_async()`、`del_device_async()`、`send_rpc_reply()`。下行时则通过 `GrpcConnector` 和 `GrpcDownlinkConverter` 把属性更新或 RPC 请求写回外部进程。

这个设计让某些 connector 可以脱离主进程运行，但进入 Gateway 中央管线之后，仍然复用同一套队列、存储、设备注册和 MQTT 发送逻辑。

**实现视角**

如果一个编码人员要按图实现这个 Gateway，关键骨架就是：先实现 `TBGatewayService` 这个编排中心；再定义 connector/converter 接口；接着实现 `ConvertedData`、report strategy、EventStorage；然后接入 ThingsBoard MQTT Gateway API；最后补齐 RPC、属性、远程配置、watcher、统计和可选 gRPC 扩展。

分析助手这次图里刻意没有画 ThingsBoard Server、ThingsBoard Edge、UI widgets 或 dashboard 之类内容，因为这些都超出了 `upstream/thingsboard-gateway` 的运行时代码边界。这里的 Gateway 可以被理解成一个独立的、协议可扩展的边缘数据与控制中转层。