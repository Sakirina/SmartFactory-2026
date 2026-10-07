# 源码构建、检查与发布

[Wiki 首页](../README.md) · [安装部署](../installation.md)

源码保持相邻的 DataTransfer 与 Platform 两个 Go 模块，Platform 通过本地 replace 引用采集类型。Frontends 构建三套页面，发布器固定公开源码和实际编译输入，再汇总程序、页面、契约、部署工具与许可材料。

## 工具和基本流程

| 工具 | 锁定条件 |
| --- | --- |
| Go | 模块声明 1.27.0，构建使用 1.27.1 |
| Node.js／npm | 24.21.0／11.19.0 |
| TypeScript | 7.0.2 CLI 与 6.0.2 兼容 API，别名见 package.json |
| Python | 3.12 及以上，契约依赖使用独立虚拟环境 |
| Docker Compose | 完整部署及外部数据库、协议和原生检查 |

工具链来源在 [toolchains.json](../../deploy/toolchains.json) 和版本文件中，Go 与 npm 依赖分别由 go.mod／go.sum 和 package-lock 固定。以下在仓库根目录执行，依赖由工具按配置获取。

1. 检查工具链并安装构建／契约依赖：

   ```sh
   python3 scripts/check-toolchains.py
   npm ci --prefix Frontends
   python3 -m venv .venv
   .venv/bin/python -m pip install -r tests/fixtures/requirements-contracts.txt
   ```

2. 执行两个模块普通测试、契约和 Python 检查：

   ```sh
   go -C DataTransfer test ./...
   go -C Platform test ./...
   .venv/bin/python scripts/check-contracts.py
   .venv/bin/python -m unittest discover -s tests -p 'test_*.py' -v
   ```

3. 核对生成客户端、前端单测、两套类型及正式产物：

   ```sh
   npm run check:api --prefix Frontends
   npm run test:unit --prefix Frontends
   npm run typecheck:compat --prefix Frontends
   npm run build --prefix Frontends
   ```

4. 使用新输出目录构建并验证 Linux 包：

   ```sh
   python3 scripts/build-release.py \
     --output /tmp/smartfactory-linux-amd64 \
     --os linux \
     --arch amd64 \
     --version 1.1.0
   python3 scripts/verify-release.py /tmp/smartfactory-linux-amd64
   ```

构建器执行前端构建，当前 dist 已经检查时可用 `--skip-frontend-build`；中断后 `--resume` 使用该构建器的既有输出和源码快照。`--os` 支持 linux／darwin，`--arch` 支持 amd64／arm64，完整容器部署使用 Linux amd64。

## 契约生成

模型、参数或路由变化后重新导出并检查：

```sh
go -C Platform run ./cmd/sf-contracts
.venv/bin/python scripts/check-contracts.py
npm run generate:api --prefix Frontends
npm run check:api --prefix Frontends
```

导出器生成 contracts/1.0 及工厂定义，前端生成器由 OpenAPI 更新类型和操作目录。检查生成差异、重复输出及对应指南，DataTransfer Protobuf 生成入口见 [Makefile](../../DataTransfer/Makefile)。

## 外部和浏览器检查

普通 Go 回归中的外部条件由各测试的环境变量决定，实际 PostgreSQL、OPC UA、HR 进程、原生 CE／Edge 和 NATS 检查需要相应服务与身份。限定 race 用于受影响并发路径，记录实际命令、环境、跳过及执行范围。

| 浏览器入口 | 运行前准备 |
| --- | --- |
| playwright.config.ts | 云端／现场、示例定义、开发凭据文件和可用 Chromium |
| workspace.spec.ts | 上述基础服务及示例设备、管理身份 |
| assistant.spec.ts | 基础服务加模型模拟器，草稿许可和示例定义 |
| playwright.phase4.config.ts | 独立业务／AI／精确值夹具，SF_PHASE4_* 参数及相应私有清单 |
| phase5.playwright.config.ts | 独立 cloud／config／edge、代理、固定目标，SF_PHASE5_CASES 及各用例夹具 |

浏览器测试使用实际页面和服务，并可能保存草稿或改变业务状态，在独立环境运行。基础脚本读取仓库运行目录的开发凭据，Chromium 路径由 `SF_BROWSER_EXECUTABLE` 指定，业务地址由 `SF_BROWSER_BASE_URL` 指定；阶段专项还读取各自配置及测试文件声明的环境，按该脚本的服务、身份和场景准备后选择相应测试。

验证脚本见 [Frontends/tests](../../Frontends/tests)、[前端夹具](../../Frontends/scripts)、[协议检查](../../scripts/verify-delivery-protocols.py)与[正式包入口检查](../../scripts/verify-delivery-runtime.py)。验收中的 Linux／浏览器组合范围记录在[版本说明](../releases/1.1.md)。

## 部署包与可追溯材料

| 材料 | 内容 |
| --- | --- |
| release-manifest.json | 应用版本、平台、来源及实际文件集合 |
| source-manifest.json | 固定公开源码及逐文件摘要 |
| build-inputs.json | 实际编译输入、前后摘要及二进制来源 |
| build-commands.json、build-logs | 命令、退出结果及日志摘要 |
| components.json、licenses | Go、npm、标准库、运行时和组件许可 |
| deploy/images.lock.json、deploy/licenses | 锁定镜像和对应来源、适用许可 |

包内提供十四个 Go 程序及三套页面，镜像另行按锁准备。npm 别名记录实际发行身份和安装位置，Go 标准库及运行时记录实际版本；许可材料对应具体组件，详细适用范围见[组件许可](../../deploy/licenses/README.md)。

Wiki 同时复制到根目录和 source/Wiki，根文档的源码链接改写为 source 下路径。包内重新构建进入 source，保留两个模块的相邻结构，构建器支持没有 Git 元数据的固定源码。

## GitHub 自动构建

[Build and release](https://github.com/Sakirina/SmartFactory-2026/actions/workflows/build-release.yml) 在 main 推送、面向 main 的 Pull Request 或手动运行时执行普通 Go 测试、Python 验证与打包测试、契约生成检查、前端单测和类型兼容，并正式打包及校验。构建器执行 TypeScript 7 类型检查和生产页面构建；该流程的实际结果显示在相应 Actions 运行中。

分支产物以快照名称保存，Artifacts 保留十四天。发布人员选择已检查的提交，创建尚未使用的版本标签并推送：

```sh
git tag -a v1.1.0 -m "SmartFactory v1.1.0"
git push origin v1.1.0
```

标签流程重新构建，归档与 sha256 上传后创建 GitHub Release，带后缀标签标记为预发布。正式来源、应用版本与发布状态查看对应运行及归档清单；已有公开版本更新采用新标签。

本页依据：[构建器](../../scripts/build-release.py)、[包校验](../../scripts/verify-release.py)、[自动工作流](../../.github/workflows/build-release.yml)、[前端命令](../../Frontends/package.json)、[打包检查](../../tests/test_release.py)。
