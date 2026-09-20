# SmartFactory

工业设备采集、数据分析、现场联动与云端业务管理平台，采用 ThingsBoard CE 4.3.1.5、ThingsBoard Edge 4.3.1.1EDGE、DataTransfer 和自研 Go 业务服务。云端控制台、边缘现场页面与管理大屏使用 React 和 TypeScript 开发。

源码按职责组织：`DataTransfer` 实现 Modbus TCP、MQTT、OPC-UA 与独立进程采集插件；`Platform` 包含云端、边缘、配置中心和辅助命令；`Frontends` 提供三套业务页面。`contracts/1.0` 保存 JSON Schema 与 OpenAPI，`examples` 提供工厂定义和 MCP 客户端，`scripts` 提供构建、部署、备份与验证工具。

业务服务通过 `/api/sf/v1` 提供数据查询、实时订阅、定义草稿与发布、控制审批、配置和审计。云边连接使用 mTLS；页面助手与 MCP 使用相同资源权限，支持查询和草稿操作。

## 构建与检查

构建需要 Go、Node.js、npm 和 Python 3。Go 模块及前端依赖版本分别由 `go.mod`、`go.sum` 和 `Frontends/package-lock.json` 固定；两个 Go 模块需要保留当前相邻目录结构。容器部署另外需要 Docker Compose。

1. 安装前端依赖并检查代码：

   ```sh
   cd Frontends
   npm ci
   npm run build
   cd ..
   ```

2. 执行两个 Go 模块的测试：

   ```sh
   go -C DataTransfer test ./...
   go -C Platform test ./...
   ```

   外部协议与数据库测试通过各测试文件声明的环境变量启用。Python 契约和 OPC-UA 测试依赖位于 `tests/fixtures/requirements-contracts.txt` 与 `requirements-opcua.txt`，可安装到单独的虚拟环境。

3. 构建 Linux x86_64 部署包并核对文件摘要：

   ```sh
   python3 scripts/build-release.py --output /tmp/smartfactory-release --os linux --arch amd64
   python3 scripts/verify-release.py /tmp/smartfactory-release
   ```

   部署包包含可执行程序、前端静态文件、源码、契约、部署工具及组件许可证。源码快照也支持在没有 Git 元数据的目录中重新构建。

## 部署流程

ThingsBoard、PostgreSQL、Kafka、NATS 与其他镜像的内容摘要保存在 `deploy/images.lock.json`。运行 `python3 scripts/pull-images.py` 查看所需镜像和传输规模，确认后添加 `--execute` 获取镜像。

1. 在 Linux x86_64 主机准备部署包与所需镜像，再生成独立的私有状态目录：

   ```sh
   python3 scripts/prepare-runtime.py --bundle /tmp/smartfactory-release --directory .local/customer --profile capacity --hosting self --simulation scenes
   ```

   `capacity` 使用一个边缘节点，`site` 使用三个边缘节点；`hosting` 可选 `self` 或 `hosted`。三节点使用默认的 `fleet` 模拟模式。

2. 初始化数据库、原生平台、云边身份与模拟设备：

   ```sh
   python3 scripts/bootstrap-runtime.py install --directory .local/customer
   ```

   生成的私有目录保存初始凭据、客户 CA、节点证书和密钥。云端 HTTPS 默认使用 8443，边缘入口从 8444 开始，完整地址保存在该目录的 `profile.json`。

3. 使用 `bootstrap-runtime.py status` 查看状态，使用 `stop` 和 `start` 操作已安装实例；这些命令同样需要 `--directory` 指定私有状态目录。

`scripts/run-memory-demo.py` 提供有时限的本机业务演示，通过 `--binary-dir` 指定当前操作系统的可执行程序。备份与恢复由 `scripts/backup-state.py` 提供，各工具可通过 `--help` 查看参数。

## 上游版本与许可证

ThingsBoard CE 固定为 `v4.3.1.5`，Edge 固定为 `4.3.1.1EDGE`；源码提交记录在 `deploy/dependencies.json`。上游许可证及来源摘要位于 [deploy/licenses](deploy/licenses)，构建器另外为 Go 与 npm 依赖生成组件清单和许可证目录。
