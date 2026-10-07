# 五个示例场景

[Wiki 首页](../README.md) · [首个完整流程](../getting-started.md)

五场景使用协议模拟设备提供输入与反馈，适用于本机演示和 `capacity --simulation scenes` 持久实例。每个场景包含对应设备、正式定义和动作映射，可以沿观测、判断、告警、执行及反馈追踪。

| 场景 | 设备与协议 | 主要字段或动作 |
| --- | --- | --- |
| 温湿度 | `climate-1`，Modbus TCP | temperature、humidity、fan、heater、humidifier、dehumidifier |
| 红外照明 | `light-1`，MQTT | presence、light |
| 气体监测 | `gas-1`，OPC UA | smoke、combustible、co、extractor |
| AGV 避障 | `agv-1`，Modbus TCP | distance、stopped、联锁与恢复 |
| 货物计数 | `counter-1`，MQTT | pulse、累计 total |

## 场景规则

温度分析采用一分钟平均值，告警达到 30 °C 触发、降至 27 °C 或以下恢复；通风在温度超过 30 °C 启动，降至 27 °C 或以下关闭。低温加热在不高于 18 °C 时启动，达到 20 °C 关闭。

湿度不高于 30 时加湿，达到 35 关闭；达到 70 时除湿，降至 65 或以下关闭，高湿告警采用相同回差参数。示例字段单位由采集测点提供，正式工艺参数在实际配置中评审。

照明读取 `presence`，无人持续两秒后关闭，延时由 debounce 节点的状态和逻辑时刻推进。气体告警使用 CRITICAL，烟雾触发／恢复参数为 5／2、可燃气 20／10、一氧化碳 30／15，排风预案使用对应上限条件。

AGV 距离不大于 50、质量无效或观测过期时触发停车，恢复要求距离至少 80，再通过控制请求核对联锁和审批。货物计数使用稳定 pulse 事件身份累计 total，重复事件重投继续使用原身份，同一时间的不同事件分别累计。

## 检查过程

1. 查看五个设备均已批准，核对输入、来源和时间；在定义页面查看选择器、阈值、动作与反馈字段。
2. 在独立模拟环境的另一终端运行检查，工具改变设备状态、产生事件和执行预案。本机演示命令为：

   ```sh
   python3 scripts/check-scenes.py \
     --state-directory .local \
     --simulator-port 18083 \
     --pulses 100 \
     --output .local/five-scenes-check.json
   ```

3. 完整五场景实例使用安装私有目录和映射端口：

   ```sh
   python3 scripts/check-scenes.py \
     --state-directory ../smartfactory-state \
     --simulator-port 19083 \
     --pulses 100 \
     --output ../smartfactory-state/five-scenes-check.json
   ```

4. 阅读报告中每次输入、反馈和耗时，回到页面查看告警周期、通知、执行及数据修订；需要处置和交接时继续[业务工作台](../user-guide/business-workbench.md)。
5. 保存需要保留的导出，再停止演示或持久实例。

`--pulses` 支持 1 至 10000，本次报告读取运行实例的存储方式。场景阈值用于展示实现行为，实际设备运行依据正式测点、工艺政策和现场联锁。

本页依据：[基础定义](../../Platform/internal/app/seed.go)、[场景定义](../../Platform/internal/app/scenes.go)、[协议模拟器](../../DataTransfer/cmd/dt-simulator)、[场景检查](../../scripts/check-scenes.py)。
