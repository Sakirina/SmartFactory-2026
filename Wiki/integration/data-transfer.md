# DataTransfer 设备接入

[Wiki 首页](../README.md) · [设备与资产](../user-guide/devices-assets.md)

DataTransfer 读取协议设备、生成统一消息并处理设备命令，边缘业务通过本地 gRPC 查询观测与命令结果。设备业务身份、资产归属和批准由平台维护，通信参数与动作映射由采集配置确定。

| 协议 | `protocol` | 配置内容 |
| --- | --- | --- |
| Modbus TCP | `modbus_tcp` | 主机、端口、单元号、寄存器、地址、值类型、比例及动作写入 |
| MQTT 设备 | `mqtt_device` | Broker、认证、观测／事件／状态／反馈主题、JSON 字段及命令主题 |
| OPC UA | `opcua` | Endpoint、会话与安全策略、证书、Node ID 和方法 |
| 独立进程 | `process` | 可执行程序路径、进程参数与协议配置 |

## 接入步骤

1. 确定设备所属边缘节点和统一 `device_id`，准备网络、认证、采样及反馈字段。
2. 按[示例配置](../../DataTransfer/configs/datatransfer.example.yaml)定义连接器、设备与 `datapoints`，填写单位、类型和采样周期。
3. 控制设备时为预案动作配置 `action_mappings`，核对参数、协议写入及结果反馈。
4. 启动采集程序，检查管理和连接器状态，再使边缘服务连接 gRPC。
5. 在所属现场批准设备，检查配置版本和采用回执，再查询原始观测及反馈。

开发环境在仓库根目录运行以下命令，配置路径相对于 DataTransfer 模块：

```sh
go -C DataTransfer run ./cmd/datatransfer \
  --config configs/datatransfer.example.yaml
```

示例使用开发模式，管理地址和协议目标按实际环境调整。`grpc.enabled`、`grpc.addr` 控制采集服务，示例为 `127.0.0.1:50051`，边缘通过 `SF_DATATRANSFER_ADDRESS` 指定该地址；跨主机时两端配置 CA、客户端／服务端证书与私钥，gRPC 反射仅在允许的开发配置启用。

## 上报、缓冲和恢复

上报策略从测点、连接器到全局依次采用，支持 `ON_RECEIVED`、`ON_CHANGE`、`ON_REPORT_PERIOD`、`ON_CHANGE_OR_REPORT_PERIOD`，数值变化可用 deadband，周期模式提供周期。

背压默认 `BP_BLOCK`，`BP_DROP_OLDEST` 可以删除旧持续观测并记录缺口；状态、业务事件和回执按相应确认机制保存。`BP_DEGRADE` 当前回退为阻塞行为。持久 buffer 的空间、期限、补发速率与批次由配置决定，处理积压时核对队列、缺口和稳定消息身份。

## 配置和命令采用

“协议与设备参数”提供目录表单及预检，正式连接器配置按版本下发；固定发布采用后由实际 MQTT、Modbus TCP、OPC UA 消费者报告运行内容。本地应用代次与固定来源分别保存，方法见[节点配置](../user-guide/node-releases.md)。

命令使用稳定 `command_id`，发送前保存动作日志，相同内容重交查询原结果；反馈保留来源、可信状态与时间。结果未知时从[控制页面](../user-guide/control.md)核对原命令，设备接入时检查协议反馈是否能够证明所需动作条件。

进程插件通过 Unix socket 与 SDK 通信，开发方法见[扩展指南](../development/extensions.md)。独立 split MQTT 北向模式见[分体配置](../../DataTransfer/configs/datatransfer.split.example.yaml)。

本页依据：[启动与配置](../../DataTransfer/internal/bootstrap)、[配置模型](../../DataTransfer/internal/config/config.go)、[采集 RPC](../../DataTransfer/proto/datatransfer/v1/datatransfer.proto)、[配置消费者](../../DataTransfer/internal/configmanager/manager.go)、[业务桥](../../Platform/internal/datatransfer/bridge.go)。
