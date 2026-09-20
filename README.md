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

## GitHub 自动构建与发布

[Build and release](https://github.com/Sakirina/SmartFactory-2026/actions/workflows/build-release.yml) 工作流在推送 `main`、提交面向 `main` 的 Pull Request 或手动运行时执行两个 Go 模块的测试、契约检查、打包内容检查和前端构建，并生成 Linux x86_64 部署包。通过检查的产物保存在该次运行的 Artifacts 中，保留 14 天；手动构建可在 Actions 页面点击 **Run workflow**。

发布版本时，为已经完成检查的提交创建版本标签并推送：

```sh
git tag -a v1.0.0 -m "SmartFactory v1.0.0"
git push origin v1.0.0
```

将示例中的 `v1.0.0` 替换为本次版本号。工作流会重新执行检查和构建，成功后自动创建 GitHub Release，附上 `smartfactory-v1.0.0-linux-amd64.tar.gz` 与对应的 `.sha256` 文件；`v1.0.0-rc.1` 这类带后缀的版本会标记为预发布。上传完成后 Release 才会公开，已经发布的版本需要使用新标签更新。

部署包包含 13 个 Go 可执行程序、三套前端页面、公开源码、契约、部署脚本和组件许可证，内部文档、赛事材料、缓存及本地凭据由打包器排除；GitHub 自动生成的源码包通过 `.gitattributes` 排除内部材料。容器镜像按 `deploy/images.lock.json` 在部署主机另行准备。发布步骤使用 GitHub 自动提供的 `GITHUB_TOKEN`，所需的 `contents: write` 权限已在工作流中声明。

下载部署包后，可以在 Linux 上核对摘要并解包：

```sh
sha256sum -c smartfactory-v1.0.0-linux-amd64.tar.gz.sha256
tar -xzf smartfactory-v1.0.0-linux-amd64.tar.gz
python3 smartfactory-v1.0.0-linux-amd64/scripts/verify-release.py smartfactory-v1.0.0-linux-amd64
```

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
