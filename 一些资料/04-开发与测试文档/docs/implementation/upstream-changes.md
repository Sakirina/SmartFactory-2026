# 上游版本、许可证与修改说明

ThingsBoard CE 使用 `v4.3.1.5`，对应提交 `2cd31ac359214f94b945546830da503ddf80070f`；ThingsBoard Edge 使用 `4.3.1.1EDGE`，对应提交 `04d9daf4a557a13483de2310e35b6493aff751fc`。`deploy/dependencies.json` 固定源码版本，`deploy/images.lock.json` 固定本次部署的镜像内容摘要和运行平台。

本次采用上游发布镜像，通过 HTTP API 创建专属租户、设备、资产、规则链与计算字段，通过 Edge RPC 和自研业务同步传递配置和数据。本仓库没有对上述固定版本附加源码补丁，原生服务的启动参数、数据库初始化和业务对象由 `scripts/prepare-runtime.py`、`bootstrap-runtime.py` 及 `Platform/internal/thingsboard` 管理。后续需要修改上游源码时，补丁须绑定上述提交，并随构建脚本和验证结果保存。

自研代码位于 `DataTransfer`、`Platform` 和 `Frontends`。公共契约位于 `contracts`，可编辑的场景定义位于 `examples`，部署与验收脚本位于 `scripts`。三套前端使用 React、TypeScript、Ant Design、React Flow 和 Apache ECharts，业务页面通过 `/api/sf/v1` 访问自研服务。

固定提交中的 ThingsBoard 根目录与前端许可证保存在 `deploy/licenses/thingsboard` 和 `deploy/licenses/thingsboard_edge`；来源 URL、提交、原始文件位置和 SHA-256 记录在 `deploy/licenses/provenance.json`。构建器还从实际 Go 模块与 npm 包复制许可证，生成 `components.json`，记录依赖版本、是否用于构建和许可证文件位置。锁文件中的其他操作系统可选包以 `installed_for_build: false` 标记，容器内部组件保留在原始镜像中。

交付包中的 `source` 包含本轮工作树源码，`release-manifest.json` 记录源码提交、构建工具版本与逐文件摘要。部署前运行 `scripts/verify-release.py` 核对文件；参赛声明的署名由参赛者在[声明函草稿](open-source-statement-draft.md)中完成。
