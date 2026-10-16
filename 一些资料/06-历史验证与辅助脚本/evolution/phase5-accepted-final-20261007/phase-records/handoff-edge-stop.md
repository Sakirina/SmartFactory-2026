# Edge 停止信号验证交接

此项由第五阶段构建交付工程师在 E5-04 验收前继续验证，当前状态为待修正、待验证。本轮 CE/Edge 原生通信、代理 TLS/mTLS/SSE 与受控资源验证已经保存证据；Edge 停止信号转发问题独立记录。

受测镜像为 `thingsboard/tb-edge@sha256:21d75ce523240bce0eef86ec3eabe289c09e3843059606bef9d24529fc0444f3`，版本 4.3.1.6EDGE，镜像为 linux/amd64、Docker 主机为 aarch64。镜像 Config.Entrypoint 为 null，Config.Cmd 为 `start-tb-edge.sh`，实际脚本路径为 `/usr/bin/start-tb-edge.sh`。运行时 PID1 为 `/bin/bash /usr/bin/start-tb-edge.sh`，Java 为 PID7；脚本读取 `/usr/share/tb-edge/conf/tb-edge.conf`，最后使用 `java -cp ...` 启动 Java 子进程，未使用 exec。

本轮停止入口为 `docker stop --time 30 sf-evolution-edge-20261004`。命令返回 0，实际容器于 2026-10-04 15:10:52 UTC 退出 137，耗满三十秒停止时限；对应时段的 Docker OOM 事件没有该 Edge 容器记录。`service-cleanup.json` 保存了该容器 ID、停止命令结果和实际 die 事件。镜像脚本及运行时父子进程关系说明，需要继续核对 PID1 向 Java 转发停止信号的处理；具体信号机制及修复尚未验证。

适用部署入口包括 `docker compose --env-file .local/native.env -f deploy/compose.native.json --profile edge stop thingsboard-edge`，以及 `python3 scripts/bootstrap-runtime.py stop --directory <prepared-deployment-directory>`。后者调用已准备目录的 compose.json 并启用 native-edge profile，停止该部署服务。`scripts/check-long-runtime.py` 的备份前停止也调用 Compose stop，但其完整长时专项已被用户排除；交付工程师只需要检查共用停止机制，按有限服务启动、停止与数据复核流程完成验证。

建议验收流程如下：

1. 审查准备部署与镜像入口，在临时服务中实现明确的 Java 停止信号传递方式，并记录实际 PID1、Java PID 和生效的 JVM 参数。
2. 通过实际部署停止入口结束 Edge，采集应用关闭日志、数据库事务结束、容器实际退出码与停止耗时；退出记录需要区分信号终止、Docker 停止时限后的强制终止和 cgroup OOM。
3. 使用保留的数据重新启动该临时服务，查询此前原始与计算遥测，验证重复消息处理和平台审计，再结束自己的服务并记录资源释放。

本轮原始诊断及运行证据位于 `.local/evolution/native-controlled-resources.json`、`.local/evolution/service-cleanup.json`，Edge 完整服务日志保存在 `/private/tmp/smartfactory-evolution-native-20261004/sf-evolution-edge-20261004-final.log`。日志权限为0600，凭据不纳入公共交接内容。
