# DataTransfer — 数据传递微服务

智能工厂物联网平台的边缘侧通信中间件:南向适配 Modbus TCP / MQTT Device / OPC-UA 等设备协议,归一化为统一 `DeviceMessage`(Protobuf),北向以 gRPC(方案A 嵌入式)或 MQTT(方案B 分体式)交付上游微服务;承接下行指令(路由、去重、重试、回执)与设备注册微服务的配置热推送。

需求/接口/设计的权威基线见仓库外层 `Baseline/数据传递微服务/`(v2.2)。

## 快速开始

```bash
# 测试(使用项目内缓存目录,见 Makefile)
make test

# 本地运行(示例配置为 development 环境)
GOPATH=$PWD/.cache/gopath GOCACHE=$PWD/.cache/go-build GOMODCACHE=$PWD/.cache/go-mod \
  go run ./cmd/datatransfer -config configs/datatransfer.example.yaml

# 健康与指标
curl localhost:8080/healthz ; curl localhost:8080/readyz ; curl localhost:8080/metrics
```

gRPC 调试(仅开发环境):配置 `environment: development` 且 `grpc.reflection: true` 后,可用 grpcurl 直接探索服务;反射默认禁用,生产配置会被启动校验拒绝,并支持工作在 `grpc.tls` 加密之上。

## 目录结构

| 路径 | 说明 |
|------|------|
| `cmd/datatransfer` | 主入口 |
| `proto/` → `gen/` | Protobuf 契约与生成代码(`make proto` 重新生成) |
| `internal/bootstrap` | 进程装配、环境区分(development/production)、优雅停机 |
| `internal/runtime` | 消息中枢:策略过滤、订阅分发、环形缓冲、指标汇总 |
| `internal/command` | Command Router:去重(DT-CMD-005)、超时、重试(FR-S-014) |
| `internal/connector` | Connector 契约 + Manager(生命周期、自动重启、背压水位、策略索引) |
| `internal/connector/{modbus,mqttdevice,opcua,sidecar}` | 内置协议与插件隔离 mock |
| `internal/strategy` | 上报策略引擎(FR-S-033~036) |
| `internal/buffer` + `internal/storage` | 方案B SQLite 两阶段缓冲;storage 为信创 StorageBackend 预留点 |
| `internal/northbound/{grpc,mqtt}` | 北向双模式适配 |
| `internal/configmanager` | 配置热加载:幂等 + entity_revision 乱序保护(FR-S-029a) |
| `internal/security` | 客户端/服务端 TLS 构造(国密 TLSProvider 预留点) |
| `internal/observability` / `internal/errors` | 管理端点、Prometheus 渲染 / 错误码(对齐接口文档 5.2) |
| `pkg/pluginapi` | Connector 插件开发契约(插件不得 import internal) |
| `configs/` | embedded 与 split 两份示例配置(均有加载回归测试守护) |
| `docs/` | 协作事项(待办权威)、交接清单、迭代审计记录、遗留问题、v5 spike |

## 文档入口

- 待办与迭代状态:[docs/协作事项.md](docs/协作事项.md)
- 交接与恢复开发:[docs/交接清单.md](docs/交接清单.md)
- 最近一轮审计与修复:[docs/iteration-p3.5-review.md](docs/iteration-p3.5-review.md)
- 跨服务依赖:仓库外层 `跨服务协作备忘.md`(CSC-001~006)
