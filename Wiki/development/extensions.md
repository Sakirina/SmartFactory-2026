# 扩展开发

[Wiki 首页](../README.md) · [源码构建](build.md)

设备读数及动作可以通过采集进程插件扩展，人员和部门变更通过组织同步插件提供，计算与判断通过正式节点目录及计算内核扩展。扩展采用所属消息、身份和契约，测试包含结果、错误和退出行为。

## 协议进程插件

主程序通过 Unix socket 和插件 SDK 执行初始化、启动、配置与命令，`SF_PLUGIN_SOCKET` 提供套接字路径。连接器管理器检查进程、连接器和设备身份，观测采用 DataValue 对应类型，回执保留稳定消息身份。

1. 参考 HTTP 插件实现初始化、观测输出及命令处理，准备可访问的本机协议模拟器。
2. 在仓库根目录构建与主机对应的插件：

   ```sh
   go -C DataTransfer build -o /tmp/sf-http-plugin ./examples/http-plugin
   ```

3. 在完整采集配置的 connectors 中加入如下片段，安装时使用实际可执行路径：

   ```yaml
   connector_id: http-process
   protocol: process
   process:
     executable: /tmp/sf-http-plugin
   connection:
     url: http://127.0.0.1:18083
   devices:
     - device_id: climate-1
   ```

4. 检查启动、观测、原命令反馈、进程异常及正常退出，再完成[现场准入](../user-guide/devices-assets.md)。

HTTP 示例使用回环模拟器 URL，通过 `/state` 读取设备，通过 `/command/{device_id}` 提交命令并读取结果。SDK 与 RPC 见 [plugin/sdk.go](../../DataTransfer/pkg/plugin/sdk.go) 和 [plugin.proto](../../DataTransfer/proto/datatransfer/plugin/v1/plugin.proto)，生命周期与身份检查见[进程连接器](../../DataTransfer/internal/connector/sidecar/process.go)。

## 组织同步插件

组织插件从标准输入取得 method、source、after、配置和当前凭据，标准输出返回 OrganizationSync。示例 sf-hr-plugin 从 HTTPS 拉取变更，回环 HTTP 用于本机检查，外部跳转和错误保存为调用失败。

1. 返回稳定同步 id、来源、递增 sequence、部门及人员，按 after 查询后续变更。
2. 在配置中心登记凭据，在“同步插件”填写可执行路径、参数、配置和凭据引用。
3. 设置补偿查询周期并启用，检查同步序号、最后运行与错误。
4. 在“组织与权限”核对人员创建、部门移动和停用，再检查资源及会签资格。

轮询周期支持一秒至一天，超时或退出失败保存原因。外部推送入口为 `/internal/plugins/{id}/organization`，采用插件推送凭据；组织和游标提交后确认，同一身份不同内容或倒退序号按冲突处理。

本节依据：[组织插件](../../Platform/internal/plugins/manager.go)、[同步结构](../../Platform/internal/plugins/organization.go)、[HTTP 适配示例](../../Platform/cmd/sf-hr-plugin/main.go)、[OrganizationSync 契约](../../contracts/1.0/OrganizationSync.schema.json)。

## 编排节点

1. 在 model 的 NodeCatalog 登记定义种类、输入输出端口、参数 Schema、单位及状态属性。
2. 在定义校验和计划编译实现类型、依赖及连接规则，再为运行内核和历史路径实现相应计算。
3. 使元数据表单、完整 JSON 与生成契约采用同一目录，保存已有扩展字段的合法行为。
4. 检查精确值、质量、状态、历史、连续模拟及失败路径，重新导出契约与客户端。
5. 涉及设备动作时通过控制层采用命令身份和持久回执，并核对恢复与现场联锁。

相关源码：[节点目录](../../Platform/pkg/model/node_types.go)、[定义校验](../../Platform/internal/engine/definitions.go)、[编译计划](../../Platform/internal/engine/plans.go)、[计算内核](../../Platform/internal/engine/runtime.go)、[连续模拟](../../Platform/internal/engine/simulation.go)、[节点表单](../../Frontends/src/metadata-form.tsx)。
