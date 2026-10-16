第五阶段发布页面与验证交接

页面入口为云端 index.html、现场 edge.html 和运行大屏 screen.html。云端与现场的“云边发布”负责固定清单、目标和批次，“节点身份与配置”展示工作负载、能力、配置及应用报告；场景模板页面的演进操作保留原实例和设备绑定。本轮专业验证已经完成，正式 Linux 包与最终独立审查按照用户要求留待目标恢复后继续。

首次固定清单可以从已经登记的正式对象开始创建。程序工件需要先由正式发布接口登记其兼容清单；规则来自当前账号能够读取的规则定义及固定版本，配置来自正式固定参数与连接器版本。没有既有发布时，页面仍能直接选择这些对象；“参考已有固定清单”用于已有组合的再编辑。

1. 打开“组合固定发布”，填写唯一标识与名称，选择正式程序工件以及规则、配置的固定版本。核对每项的摘要、依赖、适用节点与兼容条件；使用“检查组合”取得服务端校验结果，再保存固定清单。
2. 为固定清单选择已验证的节点身份，并安排发布批次。只有程序、规则、配置、能力和用途共同符合条件的目标才能提交；详情分别显示期望内容、准备、应用和实际运行结果。
3. 等待当前批次取得有效的实际报告后查看后续批次。代理联系时间和程序报告时间分别展示，实际报告超过十五秒即显示过期，已经完成的批次保留完成记录；程序恢复后刷新当前报告状态。
4. 遇到离线或失败时，查看服务端允许的暂停、继续、取消、重试及退回操作，填写处理原因。业务版本改变后提交会得到版本冲突提示；使用“核对状态并更新操作依据”读取当前版本，保留正在输入的原因与退回选择，再按当前许可提交。
5. 退回前核对旧清单声明的数据库、规则和配置兼容范围。兼容清单可以通过正式退回操作采用；超出数据库兼容范围的旧程序会显示拒绝原因及恢复说明，依照发布契约和已验证的备份恢复流程处理。

模板演进从原准备批次及实例出发，保留绑定的设备、参数和连接器来源。参数检查、已存在对象冲突及业务版本冲突会显示具体原因；成功生成的清单使用正式固定版本与摘要。五类模板的本轮组合包含二十五份规则、二十九项组件，实际隔离节点及 MQTT、Modbus TCP、OPC UA 配置消费者已经取得采用回执。规则来源固定版本与节点本地应用版本分别记录，本地执行信息会使应用摘要与来源摘要不同。

配置页面分别呈现动态采用、静态等待新进程、失败原因与实际消费者报告。保存后以正式响应、随后读取的版本和实际消费者值确认结果。工作负载身份操作保存服务端版本；停用与轮换按当前范围和凭据代际核验，新进程使用私有文件中的最新凭据。页面及导出显示密钥引用和状态，密钥明文保存在私有文件中。

三套入口的数据表、工具提示及完整导出保留原始整数、浮点数、字符串、布尔值与聚合结果。ECharts 坐标使用显示近似值，图例保留设备、测点与单位，时间采用 Asia/Shanghai。窄屏表格通过自己的水平滚动区域展示完整记录；页面整体在 390 像素宽度下没有横向滚动。

本轮构建使用 Node 24.21.0、npm 11.19.0 和 Frontends/package-lock.json；npm run build 依次检查生成客户端、原生 TypeScript 类型和生产构建，npm run typecheck:compat 检查 TypeScript 6 的兼容类型，npm run test:unit 执行适用的前端回归。最终输入与静态产物清单在 .local/evolution/release-interface/final-inputs.json，实际浏览器结果和暂停运行描述由 .local/evolution/release-interface-verified.json 索引。

实际浏览器采用 Frontends/phase5.playwright.config.ts。SF_PHASE5_DIRECTORY 指向本轮证据根目录；SF_PHASE5_RELEASE_FIXTURE 与 SF_PHASE5_NODE_FIXTURE 分别选择发布和节点的正式私有夹具入口，账号及密码从入口指定的私有文件读取。SF_PHASE5_RELEASE_CLOUD、SF_PHASE5_RELEASE_EDGE、SF_PHASE5_NODE_CLOUD、SF_PHASE5_NODE_EDGE 指定四个静态代理入口。每次执行单独设置 SF_PHASE5_EVIDENCE、SF_PHASE5_OUTPUT 与 SF_PHASE5_REPORT，避免修改历史轮次。原始状态保存为 .state.json，页面请求与截图观察保存为 .page.json。

恢复后的验证顺序如下；需要新建发布对象的脚本使用唯一标识，并通过正式接口操作，服务端与私有目录各自保留原持久状态。

1. 读取恢复后的发布入口和进程记录，检查实际监听、程序摘要及凭据归属；SF_PHASE5_CASES=resume 执行 phase5-resume.spec.ts，生成 normal-final-release.state.json，作为后续正常组合的前置输入。
2. 完成五模板的正式协议、设备、参数与连接器准备，SF_PHASE5_CASES=templates 执行 phase5-templates.spec.ts。若本轮已经保存清单而截图阶段中断，使用 template-adoption 与 phase5-template-adoption.spec.ts 继续该正式清单，生成 template-final-evolution.state.json。
3. 使用 failure-rollback 与 phase5-final-failure.spec.ts 检查子程序启动失败、后续批次停止、密钥文件还原、合法重试及兼容退回；使用 pipeline 与 phase5-pipeline.spec.ts 检查离线、普通报告保持人工操作版本、409编辑保留，以及十五秒实际报告过期与恢复。
4. 使用 template-basis-fixed 与 phase5-template-basis.spec.ts 检查延迟 HTTP 刷新保留新参数及正式提交，使用 navigation 与 phase5-navigation.spec.ts 检查同页 URL、前进、返回、参数清除及当前编辑状态。
5. 执行 Frontends/scripts/phase5-precision-fixture.py，在正式存在的设备上写入精确观测并生成 precision-resumed/cloud.state.json 与 edge.state.json；使用 precision 与 phase5-precision.spec.ts 完成表格、工具提示及完整导出，再用 surfaces 检查三入口与三种尺寸。
6. 原节点配置、身份与权限的分组为 node、credentials、identity-basis、permissions 和 permissions-editor，分别对应 phase5-node.spec.ts、phase5-credentials.spec.ts、phase5-identity-basis.spec.ts、phase5-permissions.spec.ts。它们的已通过记录和实际受测版本保留在此前检查点，本轮报告列出对应关系；恢复后的独立审查根据隔离数据和最终组合执行适用分组。

原首次发布、延迟刷新、只读目录拒绝及同页 URL 问题分别按 P5-F01 至 P5-F04 留存旧源码、实际产物与首次结果。P5-F03 首次浏览器失败只保存结果和截图，随后权限交叉核对另行证明同一身份的发布详情与报告返回 200、管理目录返回 403；这两次观察的时间和证据各自保留。旧版页面使用 before-basis-fix/dist，before-permission-fix 保存该版本的源码和关联说明。

本轮临时静态服务器完成验证后关闭。共享发布服务、节点服务和三协议消费者由主协调在两位工程师结束后统一核对暂停状态；最新 PID、数据库、程序、私有凭据路径及恢复命令从最终报告指向的进程记录读取，避免使用恢复入口形成时已经被后续正式发布替换的旧 PID。
