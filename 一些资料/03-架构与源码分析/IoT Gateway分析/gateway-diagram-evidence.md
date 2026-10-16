# ThingsBoard IoT Gateway diagram evidence map

Scope: only `upstream/thingsboard-gateway` runtime code was used as authoritative evidence. `gateway-deep-dive.html` was used as a secondary orientation source with weight 0.35, then corrected against source code. Other upstream ThingsBoard or ThingsBoard Edge product directories were excluded.

## Deliverables

- `gateway-system-architecture.svg`: canonical vector system architecture diagram.
- `gateway-system-architecture-image.png`: formal raster image deliverable, deterministically rendered from the canonical SVG after the image-model draft pass.
- `gateway-data-flow.svg`: canonical vector data-flow diagram.
- `gateway-data-flow-image.png`: formal raster image deliverable, deterministically rendered from the canonical SVG after the image-model draft pass.

Reference PNGs rendered from the SVGs are kept for visual QA:

- `gateway-system-architecture-reference.png`
- `gateway-data-flow-reference.png`

Raw image-model drafts are kept separately because small code labels can drift in generated text:

- `gateway-system-architecture-image-model-draft.png`
- `gateway-data-flow-image-model-draft.png`

## Source-to-diagram mapping

### Entry point and gateway coordinator

- `upstream/thingsboard-gateway/thingsboard_gateway/tb_gateway.py` starts `TBGatewayService` from the configured `tb_gateway.json`.
- `upstream/thingsboard-gateway/thingsboard_gateway/gateway/tb_gateway_service.py` is the runtime coordinator for connector loading, ThingsBoard client connection, device registry, queues, storage, watchers, RPC, attribute updates, and remote configuration.
- `upstream/thingsboard-gateway/thingsboard_gateway/gateway/constants.py:129` defines `DEFAULT_CONNECTORS`. The diagram uses the code value of 15 default connector types: `mqtt`, `modbus`, `opcua`, `ble`, `request`, `can`, `bacnet`, `odbc`, `rest`, `snmp`, `ftp`, `socket`, `xmpp`, `ocpp`, and `knx`.

### System architecture diagram

- Gateway bootstrap, configuration, storage selection, report strategy, updater, device filter, and thread startup are represented from `TBGatewayService.__init__` and related initialization paths in `tb_gateway_service.py`.
- Connector discovery and module loading are represented by `_load_connectors()` at `tb_gateway_service.py:861`, `__connect_with_connectors()` at `tb_gateway_service.py:1009`, and `TBModuleLoader` extension/package lookup.
- MQTT platform connectivity is represented by `TBClient` in `gateway/tb_client.py:60`, its `TBGatewayMqttClient` creation paths, and connection loop at `tb_client.py:427` and `tb_client.py:488`.
- Storage is represented by `EventStorage` and its three implementations: `MemoryEventStorage`, `FileEventStorage`, and `SQLiteEventStorage`.
- Remote management is represented by `RemoteConfigurator`, its update queue, configuration handlers, and `send_current_configuration()`.
- Optional external connector integration is represented by `TBGRPCServerManager`, `GrpcConnector`, and `GwGrpcConnector`.

### Data-flow diagram

- Uplink starts with protocol connectors calling converter logic, then `send_to_storage()` at `tb_gateway_service.py:1146`.
- Report strategy handling is represented by `ReportStrategyService.filter_data_and_send()` at `report_strategy_service.py:60`.
- Converted queue batching is represented by `__send_to_storage()` at `tb_gateway_service.py:1181` and new-format processing at `tb_gateway_service.py:1221`.
- Device lifecycle side effects are represented by `add_device()` at `tb_gateway_service.py:1918` and `del_device()` at `tb_gateway_service.py:2026`.
- Storage persistence and replay are represented by `__send_data_pack_to_storage()` in `tb_gateway_service.py`, `EventStorage`, and `__read_data_from_storage()` at `tb_gateway_service.py:1437`.
- Publish to ThingsBoard is represented by gateway and child-device publish methods in `TBGatewayMqttClient` through `TBClient`.
- Downlink RPC routing is represented by `_rpc_request_handler()` at `tb_gateway_service.py:1636`, connector `server_side_rpc_handler()`, the device RPC queue, and `send_rpc_reply()`.
- Attribute update routing is represented by `_attribute_update_callback()` at `tb_gateway_service.py:1860` and connector `on_attributes_update()`.
- Watcher and remote configuration flows are represented by `_watchers()` at `tb_gateway_service.py:482`, `__init_remote_configuration()` at `tb_gateway_service.py:637`, and `RemoteConfigurator`.
- Optional gRPC flow is represented by `TBGRPCServerManager` at `gateway/grpc_service/tb_grpc_manager.py:35`, `GrpcConnector` at `gateway/grpc_service/grpc_connector.py:22`, `GrpcUplinkConverter`, `GrpcDownlinkConverter`, and `GwGrpcConnector` at `grpc_connectors/gw_grpc_connector.py:31`.

## Accuracy notes

- The diagrams intentionally do not include ThingsBoard server internals, ThingsBoard Edge internals, UI widgets, dashboards, or non-gateway product assets.
- The Claude deep-dive page was treated as a navigation aid. When it differed from source code, source code won; the connector count is the clearest example, because the runtime default connector registry contains 15 types.
- SVG files are the reproducible source of diagram truth. The formal PNG image deliverables are raster exports from the same SVG truth source; raw image-model drafts are preserved as evidence of the image generation step.
