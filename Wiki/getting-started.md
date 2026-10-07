# 快速开始与首个完整流程

[Wiki 首页](README.md) · [安装部署](installation.md)

本机演示预先装入五个场景，启动云端、边缘、配置中心、协议模拟器、DataTransfer 和模型模拟器。业务数据保存在内存中，进程结束后释放；生成的配置和日志保留在运行目录的 `.local/`。持续保存数据使用[安装部署](installation.md)中的实例。

## 启动演示

1. 准备与主机操作系统、CPU 架构一致的程序，以及 `Frontends/dist/`。Linux x86_64 使用发布包，macOS 本机程序按[构建指南](development/build.md)生成；下面在含有 `bin/` 和 `scripts/` 的包根目录执行：

   ```sh
   python3 scripts/run-memory-demo.py --binary-dir ./bin --duration 900
   ```

   `--duration` 允许 1 至 3600 秒，默认 900 秒。终端显示 `Memory demo ready` 后访问页面，保持该终端运行；`Ctrl+C` 提前结束演示。

2. 使用 `viewer` 登录云端 `http://127.0.0.1:8090/`，初始密码读取 `.local/development-credentials.json` 的 `password`。需要操作草稿、处置和审批时，根据下面流程使用初始化的 `engineer`、`leader` 或 `admin`。

3. 打开现场 `http://127.0.0.1:8091/edge` 和大屏 `http://127.0.0.1:8090/screen`，核对页面中的节点身份；配置服务 8092 供程序订阅配置，云端提供管理大屏入口。

## 以温度场景串联业务

1. 在“设备与资产”找到 `climate-1`，核对所属节点、批准状态、采样周期和设备版本；进入“数据监控”，选择 `temperature`、最近一小时和原始粒度，点击“查询历史”。

2. 阅读精确观测、单位、设备时间和质量，再查看来源与数据修订。进入下一页和“导出完整精确数据”可以继续检查同一固定时间范围，查询及订阅方法见[数据指南](user-guide/data.md)。

3. 使用 `engineer` 进入“数据分析”，打开示例温度分析的“编辑与评审”。核对输入选择器和一分钟聚合，保存草稿后点击“校验”，再进入“模拟运行”，提供一组连续温度观测，检查逐点结果与节点解释。

4. 打开评审窗口，查看正式版本与草稿的语义差异及影响范围。确认需要采用本次修改后，由拥有发布许可的人员完成发布；数据目录中可以查看分析输出的字段与版本。具体节点、版本冲突和恢复历史内容的方法见[定义编排](user-guide/definitions.md)。

5. 在独立模拟环境的另一终端执行场景检查，产生输入变化、告警和设备动作，并保存检查结果：

   ```sh
   python3 scripts/check-scenes.py \
     --state-directory .local \
     --simulator-port 18083 \
     --pulses 100 \
     --output .local/five-scenes-check.json
   ```

   工具依次改变温湿度、人员存在、气体和 AGV 状态，并生成货物计数。持久五场景实例将私有目录替换为安装目录、模拟器端口替换为 19083，完整含义见[五场景教程](tutorials/five-scenes.md)。

6. 在“告警处置”打开温度告警，查看异常周期、来源、人工处置版本及当前允许操作，填写理由后确认或分配责任。进入“工单与交接”创建关联工单，记录处理事项；交接时由当前责任人或管理员选择后续责任人，并保存已阅读的告警或执行依据。

7. 在“审批与执行”查看场景形成的预案和命令反馈。体验人工控制时，使用 `engineer` 发起已发布的普通预案，由独立工程师及符合组织关系的 `leader` 账号完成所需会签，再正式下发；具体风险、联锁与现场确认条件见[控制指南](user-guide/control.md)。

8. 打开执行详情，把设备步骤、原命令、反馈时间线与数据页面的 `fan` 等反馈字段对应起来；管理员在“业务审计”查询相同请求并校验记录。随后可以在[历史分析与后台任务](user-guide/history-tasks.md)查看回放、版本比较和任务游标，或使用[AI 助手](user-guide/assistant.md)查询当前资源并检查工具依据。

这个流程把观测、规则采用、人员处理和设备反馈关联到同一组对象。演示采用生成的账号与模拟设备，实际环境依照组织授权、正式设备配置和工艺条件完成相应步骤；演示结束前保存需要继续阅读的导出和检查文件。

## 继续使用

程序版本与配置的部署进入[节点配置与分批发布](user-guide/node-releases.md)，对外系统对接进入[HTTP API](api/rest.md)，运行维护进入[备份与恢复](operations/backup-restore.md)。页面刷新、地址和功能导航见[页面指南](user-guide/console.md)。

本页依据：[演示启动器](../scripts/run-memory-demo.py)、[示例人员与定义](../Platform/internal/app/seed.go)、[场景检查](../scripts/check-scenes.py)、[页面导航](../Frontends/src/shell.tsx)。
