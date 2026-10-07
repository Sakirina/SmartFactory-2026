# SmartFactory

SmartFactory 是面向工业现场的设备采集、数据分析与业务协作平台。设备观测通过 DataTransfer 进入边缘服务，现场执行已发布规则，云端汇集数据、定义和业务记录；使用者可以从同一设备继续追踪分析结果、告警处置、工单交接、控制命令和实际反馈。

1.1 系列提供云端控制台、现场工作台和管理大屏，包含连续草稿模拟、历史回放与版本比较、持久后台任务、AI 调查、节点身份及分批发布。入口、安装条件和首个完整流程见 [Wiki 首页](Wiki/README.md)，版本与验证范围见 [1.1 版本说明](Wiki/releases/1.1.md)。

## 开始使用

| 目标 | 入口 |
| --- | --- |
| 在本机体验完整示例流程 | [快速开始](Wiki/getting-started.md) |
| 在 Linux x86_64 安装持久实例 | [安装部署](Wiki/installation.md) |
| 连接实际设备、维护资产与规则 | [设备与资产](Wiki/user-guide/devices-assets.md)、[定义编排](Wiki/user-guide/definitions.md) |
| 处理告警、交接与设备执行 | [业务工作台](Wiki/user-guide/business-workbench.md)、[审批与控制](Wiki/user-guide/control.md) |
| 更新节点程序、规则及配置 | [节点配置与分批发布](Wiki/user-guide/node-releases.md) |
| 对接接口或维护运行环境 | [API](Wiki/api/rest.md)、[配置](Wiki/reference/configuration.md)、[备份与恢复](Wiki/operations/backup-restore.md) |
| 构建程序或开发扩展 | [源码构建](Wiki/development/build.md)、[扩展开发](Wiki/development/extensions.md) |

[GitHub Releases](https://github.com/Sakirina/SmartFactory-2026/releases) 提供带版本标签的 Linux amd64 归档和 SHA-256 校验文件。取得部署包后，在同一目录执行校验与解包；下面以 `v1.1.0` 文件名说明命令形式，所选归档和校验文件使用同一版本。

```sh
sha256sum -c smartfactory-v1.1.0-linux-amd64.tar.gz.sha256
tar -xzf smartfactory-v1.1.0-linux-amd64.tar.gz
cd smartfactory-v1.1.0-linux-amd64
python3 scripts/verify-release.py .
python3 scripts/run-memory-demo.py --binary-dir ./bin --duration 900
```

出现 `Memory demo ready` 后，云端、现场和大屏分别位于 `http://127.0.0.1:8090/`、`http://127.0.0.1:8091/edge`、`http://127.0.0.1:8090/screen`。初始密码由脚本生成，保存在运行目录的 `.local/development-credentials.json`；详细操作和演示的保存方式见[快速开始](Wiki/getting-started.md)。

## 程序与部署

| 目录 | 职责 |
| --- | --- |
| `DataTransfer/` | Modbus TCP、MQTT、OPC UA 及独立进程采集插件，观测与命令确认 |
| `Platform/` | 云端、边缘、配置中心、发布代理、业务计算与持久存储 |
| `Frontends/` | React 与 TypeScript 页面及生成的 API 客户端 |
| `contracts/1.0/` | OpenAPI 3.1 与 JSON Schema 2020-12 |
| `examples/` | 工厂定义与 MCP 客户端 |
| `scripts/` | 构建、部署准备、启停、备份和专项检查 |
| `deploy/` | 工具链、镜像锁及来源和许可材料 |
| `Wiki/` | 公开使用、运维与开发文档 |

完整部署使用 ThingsBoard CE `4.3.1.6`、Edge `4.3.1.6EDGE`、PostgreSQL `18.6`、Kafka `4.3.1`、NATS `2.14.7` 和 Envoy `1.39.1`。程序归档包含前端、源码、契约、脚本和许可证，容器镜像按照 [images.lock.json](deploy/images.lock.json) 另行准备；部署工具生成独立私有目录、随机凭据、客户 CA 和节点身份。

业务接口使用 `/api/sf/v1`。云边通信使用 mTLS，配置按工作负载身份分发，页面助手和 MCP 按人员资源权限调用工具。已确认的规则与配置可以在通信暂时中断时继续使用，恢复流程按持久游标、固定版本和原请求身份核对。

## 构建与发布

本仓库锁定 Go `1.27.1`、Node.js `24.21.0`，Python 要求 `3.12` 及以上版本。两个 Go 模块保留相邻目录结构，前端依赖由 `Frontends/package-lock.json` 固定；完整检查、跨平台构建和依赖准备见[构建指南](Wiki/development/build.md)。

```sh
python3 scripts/check-toolchains.py
npm ci --prefix Frontends
npm run build --prefix Frontends
go -C DataTransfer test ./...
go -C Platform test ./...
python3 scripts/build-release.py --output /tmp/smartfactory-release --os linux --arch amd64 --version 1.1.0
python3 scripts/verify-release.py /tmp/smartfactory-release
```

[Build and release](https://github.com/Sakirina/SmartFactory-2026/actions/workflows/build-release.yml) 在推送 `main`、面向 `main` 的 Pull Request 和手动运行时生成快照产物，版本标签触发 GitHub Release。正式版本使用 `v1.1.0` 这类标签，带 `-rc.1` 后缀的标签标记为预发布；发布包中的清单记录实际应用版本、目标平台、源码和逐文件摘要。

1.1 功能验收覆盖实际数据库、协议、独立程序及浏览器组合，Linux 包入口检查涵盖首次代理转换、规则和配置往返、缓存启动及保留数据再次启动。完整 UOS 安装恢复、单节点与三节点连续断云 24 小时、受限链路补传补算及实际 24 小时容量测量按目标环境专项执行，环境和检查条件见[版本说明](Wiki/releases/1.1.md)。

组件来源及许可保存在 [deploy/licenses](deploy/licenses/README.md)，构建器另外生成 Go、npm 和运行时组件清单。历史发布内容见[1.0 系列版本说明](Wiki/releases/1.0.md)。
