import fs from 'node:fs/promises';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { Presentation, PresentationFile, FileBlob } from '@oai/artifact-tool';
import { GlobalFonts } from '@napi-rs/canvas';

const root='/path/to/SmartFactory-2026';
const build=path.join(root,'.local/competition-materials-20261007');
const assets=path.join(root,'output/playwright/competition-materials');
const skill='/home/developer/.codex/plugins/cache/openai-primary-runtime/presentations/26.904.11930/skills/presentations';
const python='/home/developer/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3';
const revision=process.env.SF_MATERIALS_REVISION||'01';
const preview=path.join(build,'pptx-review-'+revision);
const validated=path.join(build,'validated');
await fs.mkdir(preview,{recursive:true}); await fs.mkdir(validated,{recursive:true});
for(const f of ['/System/Library/Fonts/STHeiti Light.ttc','/System/Library/Fonts/STHeiti Medium.ttc']){
 if(!GlobalFonts.registerFromPath(f)) throw new Error('Font could not be registered: '+f);
}
const font='Heiti SC';
const {finalizePresentation,applyPresentationChartFont}=await import(pathToFileURL(path.join(skill,'container_tools/artifact_tool_utils.mjs')).href);
const deck=Presentation.create({slideSize:{width:1280,height:720}});
const color={ink:'#102C3C',muted:'#4C626C',teal:'#147D73',paper:'#FCFCF9',white:'#FFFFFF',pale:'#ECF3F2'};
const publicBase='https://github.com/Sakirina/SmartFactory-2026/blob/v1.1.0/';
const nativeTables=[];
const allCopy=[];
function text(s,value,x,y,w,h,size=28,fill=color.ink,bold=false){
 const shape=s.shapes.add({geometry:'textbox',position:{left:x,top:y,width:w,height:h},fill:'none',line:{fill:'none',width:0}});
 shape.text=value;
 shape.text.style={typeface:font,fontSize:size,color:fill,bold,autoFit:'none'};
 allCopy.push(value);
 return shape;
}
function slide(title,narrative,refs=[],dark=false){
 const s=deck.slides.add();s.background.fill=dark?color.ink:color.paper;
 text(s,title,64,42,1150,78,46,dark?color.white:color.ink,true);
 const num=deck.slides.items.length;
 text(s,String(num).padStart(2,'0'),1192,674,42,28,16,dark?'#A6C2CA':'#81929A');
 s.speakerNotes.textFrame.setText(narrative+'\n\n资料来源：\n'+refs.map(r=>r.startsWith('http')?r:publicBase+r).join('\n'));
 return s;
}
async function image(s,name,x,y,w,h,alt,crop){
 s.images.add({blob:new Uint8Array(await fs.readFile(path.join(assets,name))),contentType:'image/png',alt,fit:'contain',position:{left:x,top:y,width:w,height:h},...(crop?{crop}:{})});
}
function table(s,values,widths,y=165,height=425,size=26,x=64){
 const width=widths.reduce((a,b)=>a+b,0);
 const t=s.tables.add({rows:values.length,columns:values[0].length,left:x,top:y,width,height,columnWidths:widths,values});
 t.borders.assign({style:'solid',fill:'#D2DDDF',width:1});
 t.cells.block({row:0,column:0,rowCount:values.length,columnCount:values[0].length}).assign({textStyle:{typeface:font,fontSize:size,color:color.ink},margins:{top:12,bottom:12,left:17,right:17},anchor:'center'});
 t.cells.block({row:0,column:0,rowCount:1,columnCount:values[0].length}).assign({fill:'#173D4A',textStyle:{typeface:font,fontSize:size,color:'#FFFFFF',bold:true}});
 for(let r=1;r<values.length;r++) t.cells.block({row:r,column:0,rowCount:1,columnCount:values[0].length}).assign({fill:r%2===0?'#EFF4F4':'#FFFFFF'});
 nativeTables.push(deck.slides.items.length);
 allCopy.push(...values.flat());
 return t;
}

// 01
let s=deck.slides.add();s.background.fill=color.ink;
text(s,'SmartFactory',72,157,1138,115,88,color.white,true);
text(s,'工业现场数据与业务协作平台',77,312,1124,88,48,color.white);
text(s,'产品版本 1.1.0',79,568,710,46,27,'#9FCAC4');
text(s,'2026 年 10 月',79,620,710,38,23,'#D0E1E3');
s.speakerNotes.textFrame.setText('SmartFactory 面向设备监测、现场联动和日常运维。平台把观测、规则、执行与人员协作关联到同一批业务对象，使用者能够从一次异常继续追踪到控制反馈和工单责任。\n\n资料来源：\n'+publicBase+'README.md');

// 02
s=slide('设备观测与现场工作',
 '工业现场存在多种协议和不同岗位。SmartFactory 以统一设备身份组织数据，再把规则、告警、控制和工单连接到相应的业务对象。云端处理全厂协作，现场服务负责本地采集和已发布规则。配图为应用运行界面，采用本机五场景示例数据。',
 ['README.md','Wiki/user-guide/console.md']);
text(s,'统一设备与业务记录',64,176,425,74,34,color.teal,true);
text(s,'工程师查看规则和历史数据，现场人员核对设备及反馈，管理人员追踪异常和责任。',64,275,421,184,29);
text(s,'同一次观测，可以继续追踪到告警、执行和工单。',64,505,423,113,29);
await image(s,'cloud-live.png',518,157,704,454,'云端运行概览，五场景示例数据');
text(s,'云端运行概览  示例数据',523,626,682,35,22,color.muted);

// 03
s=slide('五类工厂场景',
 '五类模板覆盖环境、人员感应、气体、移动设备和事件计数。温湿度采用阈值及回差，照明采用存在状态和延时，危气分别判断烟雾、可燃气和一氧化碳，AGV 结合距离和联锁，计数按稳定事件身份去除重复。各模板绑定实际设备和正式连接器，再生成可评审草稿。',
 ['Wiki/tutorials/five-scenes.md','Wiki/user-guide/business-workbench.md']);
table(s,[['场景','观测与判断','现场结果'],['温湿度与通风','温度、湿度、上下限与回差','告警与环境调节'],['红外照明','人员存在和延时状态','有人点亮，无人延时关闭'],['危气监测','烟雾、可燃气体、一氧化碳','异常告警与排风预案'],['AGV 避障','距离、质量与物理联锁','障碍停车和条件恢复'],['货物计数','脉冲事件与稳定事件身份','有效累计计数']],[230,463,459],158,438,26);
text(s,'每个场景保留设备、参数、规则版本和实际处理记录。',66,623,1134,47,27,color.muted);

// 04
s=slide('云端与现场的系统架构',
 'DataTransfer 就近采集设备，边缘业务服务保存观测并执行规则和控制，云端汇集数据与业务记录。独立配置中心按工作负载身份分发固定内容，发布代理核验工件并管理 Edge 子进程。ThingsBoard CE 和 Edge 承担原生实体、遥测和告警投影，多节点预案通过 NATS JetStream 维护执行权与签名检查点。',
 ['Wiki/architecture/overview.md']);
table(s,[['位置','主要程序','职责'],['设备采集','DataTransfer','协议读取、统一观测、命令结果'],['边缘现场','sf-edge、ThingsBoard Edge','现场规则、控制、存储与离线授权'],['云端平台','sf-cloud、ThingsBoard CE','全厂查询、人员、业务处置与发布'],['配置与更新','sf-config、发布代理','固定配置、身份核验与逐批运行']],[180,431,541],158,386,25);
text(s,'云边通过 mTLS 认证节点，并按持久身份、确认和游标同步。',66,572,1140,53,28);
text(s,'多节点现场使用 NATS 三副本维护执行权和签名检查点。',66,632,1138,40,25,color.muted);

// 05
s=slide('设备接入与数据质量',
 '设备需要由所属现场核对并批准，采集桥检查设备身份、节点归属和配置版本后保存观测。协议参数和扩展字段在版本化表单中维护。数据同时保留精确值、单位、质量和时间，图表显示、精确表格与完整导出各自采用适合的表达。',
 ['Wiki/user-guide/devices-assets.md','Wiki/user-guide/data.md','Wiki/integration/data-transfer.md']);
table(s,[['协议','提供的采集与操作能力'],['Modbus TCP','寄存器、位字段、字节顺序、转换与反向编码'],['MQTT','主题与载荷映射、状态或事件采集、命令发送'],['OPC UA','读取、订阅、方法调用、签名加密与证书配置']],[254,898],158,304,27);
text(s,'观测保留精确值与来源',66,510,532,59,33,color.teal,true);
text(s,'表格、工具提示和导出保留原值，查询同时展示质量与缺口。',66,586,530,87,27);
text(s,'设备与配置具有独立版本',684,510,527,59,33,color.teal,true);
text(s,'保存、投递和实际采用分别留下记录，冲突时继续核对已阅版本。',684,586,525,87,27);

// 06
s=slide('规则编排与连续模拟',
 '数据分析、告警和联动预案共用定义结构与节点目录。草稿校验检查类型、端口、表达式和依赖，连续模拟使用正式计算内核，逐条显示输入、状态变化和动作意图。语义评审检查参数、政策和引用的变化，再由有权限人员采用新版本。配图为一分钟平均温度的示例草稿。',
 ['Wiki/user-guide/definitions.md','Wiki/user-guide/history-tasks.md']);
await image(s,'editor-focus.png',63,140,933,508,'一分钟平均温度可视化规则编辑器');
text(s,'草稿修订',1030,191,183,55,29,color.teal,true);
text(s,'节点与参数\n类型与依赖',1030,268,182,97,26);
text(s,'逐点模拟',1030,393,182,55,29,color.teal,true);
text(s,'状态变化\n动作意图\n语义差异',1030,467,182,126,26);
text(s,'模拟保存输入、历史、资产版本和计划摘要，可导出复现文件。',66,651,1127,37,24,color.muted);

// 07
s=slide('历史回放与版本比较',
 '历史分析固定输入快照和计算条件。回放采用已发布规则，同输入比较让两个版本共享观测、历史、排序和初始状态，影子候选配对对应的正式结果。任务保留原业务身份与检查点，失败重试继续已经保存的进度。默认原始观测保留 30 天，分钟 90 天、小时 365 天、日汇总及告警 1095 天，可由配置调整。',
 ['Wiki/user-guide/history-tasks.md','Wiki/user-guide/data.md']);
table(s,[['分析方式','固定条件','使用者查看的结果'],['历史回放','已发布规则与输入快照','逐点结果、节点解释和修订'],['版本比较','同一输入、历史和初始状态','两个规则版本的结果差异'],['影子候选','候选规则与对应正式来源','候选与正式结果的配对变化']],[220,429,503],166,302,26);
text(s,'任务沿原身份与检查点继续',66,523,1140,59,34,color.teal,true);
text(s,'已提交结果、阶段和游标持久保存；重试继续原任务，取消处理后续计算。',66,607,1137,67,28);

// 08
s=slide('控制审批与设备反馈',
 '普通即时控制需要工程师和符合组织关系的领导，业务强制增加为两名工程师及一名领导，安全强制还需要指定现场安全员在目标节点二次认证。每人承担一个会签角色。默认会签窗口五分钟，下发后十秒内开始，物理联锁在相应步骤前检查。结果未知时核对原命令和反馈，再恢复剩余步骤。',
 ['Wiki/user-guide/control.md']);
table(s,[['请求类别','默认条件'],['普通即时控制','一名工程师及一名符合组织关系的领导'],['业务强制执行','两名工程师及一名符合组织关系的领导'],['安全强制执行','上述会签，加指定安全员在目标现场二次认证']],[294,858],158,306,27);
text(s,'5 分钟',67,505,273,66,46,color.teal,true);
text(s,'默认会签窗口',67,585,278,45,26,color.muted);
text(s,'10 秒',402,505,270,66,46,color.teal,true);
text(s,'下发后的启动期限',402,585,330,45,26,color.muted);
text(s,'原命令与实际反馈',822,517,391,60,31,color.teal,true);
text(s,'核对结果后恢复剩余步骤',822,591,391,71,25);

// 09
s=slide('告警处置与工单交接',
 '告警的持续观测版本和人工处置版本分别保存，处置人员根据已阅依据确认或分配责任。工单关联告警与控制执行。交接时提交新的责任人、未完成事项和已经阅读的业务依据，责任更新、交接请求及审计共同保存。配图是独立业务示例中的工单详情。',
 ['Wiki/user-guide/business-workbench.md']);
text(s,'责任与依据一同保存',65,181,402,66,33,color.teal,true);
text(s,'告警记录异常周期，工单记录后续处理和责任归属。',65,287,404,146,29);
text(s,'交接保留未完成事项、已阅依据和责任变化，人员按当前状态继续操作。',65,485,404,157,28);
await image(s,'work-detail-focus.png',499,215,717,370,'工单详情与处理记录局部，示例数据');
text(s,'工单详情与处理记录  示例数据',506,637,699,34,22,color.muted);

// 10
s=slide('AI 调查与可查看的依据',
 'AI 助手以当前人员身份查询数据和业务对象，工具结果保留实际进入模型的 JSON、来源版本和内容摘要。每次发送建立持久调查，网络断开后可以重新读取。引用在打开时重新检查权限，默认保留七天，过期或失去权限后清除相应内容。MCP 提供相同的读取和草稿应用服务，发布由人员评审完成。',
 ['Wiki/user-guide/assistant.md']);
text(s,'查询围绕实际业务对象展开',66,169,1115,65,34,color.teal,true);
text(s,'设备与观测、规则与草稿、控制执行、历史结果均可作为工具资料。',66,252,1117,94,30);
table(s,[['使用者看到的内容','平台保存的依据'],['调查回答与引用','实际工具 JSON、资源版本与内容摘要'],['调用状态与失败原因','调查身份、时间、预算和终止状态'],['草稿修改与模拟','草稿修订、校验结果和语义差异']],[408,744],386,231,25);
text(s,'MCP 复用人员资源许可，规则调整继续经过页面评审。',66,646,1119,38,25,color.muted);

// 11
s=slide('固定发布与逐批运行',
 '发布清单固定程序、规则、配置及连接器内容，声明平台和数据库兼容条件。独立代理核验工件和目标后管理实际 Edge 进程，协调器根据有效运行报告推进下一批次。失败恢复沿原任务和授权代次继续，程序回退按数据库兼容条件使用配套备份。',
 ['Wiki/user-guide/node-releases.md','Wiki/operations/backup-restore.md']);
table(s,[['发布对象','固定内容'],['程序','应用版本、平台、文件摘要和兼容声明'],['规则与参数','来源版本、内容摘要及节点实际采用版本'],['节点身份','工作负载类型、凭据代次、节点归属和授权'],['批次报告','准备结果、实际进程、运行摘要与失败原因']],[265,887],158,360,27);
text(s,'先核验并启动当前批次，取得有效运行报告后继续下一批次。',66,557,1131,84,31);
text(s,'切换保留原数据库与主密钥，失败处理继续记录原因和当前允许操作。',66,650,1129,38,24,color.muted);

// 12
s=slide('现场工作台',
 '现场页面随边缘部署，读取所属节点的观测与业务状态。人员在设备所在现场核对资产、参数、联锁和反馈，并使用有效的离线权限处理已发布规则相关工作。云端与现场各自保存记录，通信恢复后按游标和原请求身份同步核对。配图为 430 像素宽度的移动页面和五场景示例数据。',
 ['Wiki/user-guide/console.md','Wiki/user-guide/control.md','Wiki/architecture/overview.md']);
await image(s,'edge-mobile.png',112,143,248,520,'430 像素宽的现场移动工作台，五场景示例');
text(s,'在设备现场核对与操作',458,185,747,65,36,color.teal,true);
text(s,'设备批准、数据监控、控制确认与结果反馈共用现场业务记录。',458,303,735,132,31);
text(s,'通信暂时中断时，现场继续使用已发布规则和有效权限，随后按保存的身份与游标核对。',458,476,735,152,30);

// 13
s=slide('管理大屏与来源状态',
 '大屏与业务页面使用同一数据和授权，可配置指标及趋势，进一步打开精确数据详情。来源通信、质量、缺口和补传各自表达对应状态。配图为本机五场景示例，示例中的温湿度、气体、AGV 和计数测点展示在同一界面。',
 ['Wiki/user-guide/dashboards.md','Wiki/user-guide/data.md'],true);
await image(s,'screen-live.png',62,140,1156,490,'五场景管理大屏，示例数据');
text(s,'指标与趋势可继续查看精确值、质量、来源和修订记录。',65,648,1127,42,27,'#D0E1E3');

// 14
s=slide('可追踪的业务记录',
 'SmartFactory 的业务能力围绕原始身份与保存依据组织。消息确认、设备命令、规则计算、人员交接和节点发布分别保存继续处理所需的信息。这样，使用者在重试、恢复或变更后能够查到本次操作沿用了哪一份内容、已经完成哪些工作以及接下来允许做什么。',
 ['Wiki/architecture/overview.md','Wiki/user-guide/control.md','Wiki/user-guide/history-tasks.md','Wiki/user-guide/business-workbench.md']);
table(s,[['业务对象','保留的依据','恢复或变更后的处理'],['观测与消息','身份、质量、来源和提交游标','确认、重投与去重'],['控制执行','原命令、动作日志和设备反馈','核对原结果，恢复剩余步骤'],['规则与计算','规则版本、资产版本和输入快照','回放、比较和历史修订'],['工单交接','责任人、未完成事项和已阅版本','按当前状态继续处理'],['节点发布','固定内容、批次和运行报告','按报告推进，沿原任务恢复']],[205,466,481],160,464,25);

// 15
s=slide('1.1 系列功能验证',
 '1.1 系列五阶段共 45 项功能验收完成。同一固定前端产物完成 39 项独立浏览器检查，Linux 程序包入口完成 55 项检查，包括实际 PostgreSQL、代理节点、DataTransfer、首次转换、规则配置往返、缓存启动和保留原数据再次启动。协议与原生专项覆盖消息、mTLS、OPC UA、插件和配套恢复。完整 UOS 长时间专项按目标环境另行执行。',
 ['Wiki/releases/1.1.md']);
text(s,'45',66,194,330,126,99,color.teal,true);
text(s,'项功能验收',69,335,330,58,32,color.ink,true);
text(s,'五阶段业务能力\n规则、控制、业务与发布',69,426,330,116,26,color.muted);
text(s,'39',488,194,330,126,99,color.teal,true);
text(s,'项浏览器检查',491,335,341,58,32,color.ink,true);
text(s,'同一固定前端产物\n实际页面操作与权限变化',491,426,341,116,26,color.muted);
text(s,'55',908,194,304,126,99,color.teal,true);
text(s,'项 Linux 包入口检查',911,335,306,87,30,color.ink,true);
text(s,'独立数据库与程序\n缓存启动与保留数据重启',911,444,306,116,25,color.muted);
text(s,'协议、数据库、消息故障与原生恢复分别记录测试环境和受测输入。',68,623,1119,56,27);

// 16
s=slide('设备采集性能基线',
 '本页采用 2026 年 9 月 20 日的 DataTransfer 独立程序历史构建测量。环境为 macOS arm64、10 个逻辑 CPU、Go 1.27.0，200 台 Modbus、200 台 MQTT、100 台 OPC UA 模拟设备，预热 60 秒后持续测量 3600.012743625 秒。输入和保存均为 18654020 条单测点消息，平均每秒 5181.6538797072435 条，最大 RSS 61341696 字节，1000 次控制 p99 18.584 毫秒。测试二进制 SHA-256 为 ace2419e6a30a6ee8cdaee421e3a5f69ff5ca241fc5d8683056d6556578cfd7e。该记录用于后续目标环境对照。\n测量记录：capacity-disk-60m.json，产品白皮书第 11 页。',
 ['Wiki/releases/1.0.md','docs/evidence/capacity-disk-60m.json']);
text(s,'500 台模拟设备，持续 60 分钟',66,146,1140,59,32,color.teal,true);
const chart=s.charts.add('bar',{
 position:{left:64,top:263,width:709,height:280},
 categories:['需求值','测量值'],
 series:[{name:'单测点消息每秒',values:[5000,5181.65],fill:color.teal,dataLabelOverrides:[{idx:0,text:'5000',textStyle:{typeface:font,fontSize:25}},{idx:1,text:'5181.65',textStyle:{typeface:font,fontSize:25}}]}],
 barOptions:{direction:'bar',grouping:'clustered'},hasLegend:false,
 xAxis:{textStyle:{typeface:font,fontSize:25}},
 yAxis:{numberFormatCode:'0',min:0,max:6500,majorUnit:2000,textStyle:{typeface:font,fontSize:24}},
 dataLabels:{showValue:true,position:'outEnd',textStyle:{typeface:font,fontSize:25}},
});
applyPresentationChartFont(chart,{fontFamily:font});
text(s,'条单测点消息／秒',67,225,701,38,24,color.muted);
text(s,'58.50 MiB',847,239,374,76,49,color.teal,true);
text(s,'采集进程最大 RSS',848,326,369,52,28,color.muted);
text(s,'18.584 ms',847,428,374,76,49,color.teal,true);
text(s,'1,000 次控制的 p99',848,518,370,54,28,color.muted);
text(s,'累计输入与保存均为 18,654,020 条消息。',65,591,1136,46,29);
text(s,'2026-09-20  macOS arm64  独立采集程序历史构建',66,651,1108,35,23,color.muted);

// 17
s=slide('部署交付与开源组件',
 'SmartFactory 1.1.0 提供 Linux amd64 程序归档、校验文件、三套前端、源码、契约、脚本和许可。镜像按固定摘要另行准备。产品以 ThingsBoard CE 和 Edge 作为原生平台，项目自有采集与业务能力分别组织。UOS Server V20 x86_64 的完整安装恢复、24 小时断云、受限链路恢复及容量测量仍待在目标环境执行。',
 ['Wiki/installation.md','Wiki/releases/1.1.md','deploy/licenses/README.md','https://github.com/Sakirina/SmartFactory-2026/releases/tag/v1.1.0']);
table(s,[['交付部分','内容'],['程序与页面','Linux amd64，云端、现场和大屏'],['运行组件','ThingsBoard CE / Edge 4.3.1.6 系列'],['数据库与消息','PostgreSQL 18.6、Kafka 4.3.1、NATS 2.14.7'],['维护资料','源码、API 契约、部署脚本、Wiki、组件与许可']],[272,880],157,356,26);
text(s,'目标环境专项',67,555,1111,58,32,color.teal,true);
text(s,'UOS 完整安装恢复、24 小时断云、受限链路恢复与容量测量，待目标环境执行。',67,621,1110,63,25);

// 18
s=slide('温湿度场景演示',
 '演示沿着一次温度异常展开。先在现场核对设备和测点，再打开分析草稿进行模拟及评审，然后查看告警与处理工单，最后由相应账号完成控制审批并核对设备反馈。现场操作遵循演示预案配置和物理联锁。产品安装、人员角色和五场景操作见配套使用文档。',
 ['Wiki/getting-started.md','Wiki/tutorials/five-scenes.md','Wiki/user-guide/control.md'],true);
const steps=[
 ['1','设备与观测','核对设备、协议、采样时间和数据质量'],
 ['2','规则与模拟','运行连续样本，检查节点解释与版本差异'],
 ['3','告警与工单','确认异常，记录责任人与现场处理事项'],
 ['4','控制与反馈','完成相应审批，核对原命令及设备反馈'],
];
for(let i=0;i<steps.length;i++){
 const y=159+i*124;
 text(s,steps[i][0],73,y,57,68,44,'#8FD0C6',true);
 text(s,steps[i][1],163,y+1,397,58,34,color.white,true);
 text(s,steps[i][2],574,y+8,633,81,29,'#D5E5E8');
}

const candidate=path.join(build,'candidate-'+revision+'.pptx');
const checked=path.join(validated,'SmartFactory参赛演示-'+revision+'.pptx');
await (await PresentationFile.exportPptx(deck)).save(candidate);
await finalizePresentation({
 workspaceDir:root,candidatePath:candidate,finalPath:checked,pythonExecutable:python,
 integrityValidatorPath:path.join(skill,'container_tools/inspect_presentation_package_integrity.py'),
 layoutValidatorPath:path.join(skill,'container_tools/inspect_presentation_layout_geometry.py'),
 layoutArgs:['--expected-slide-size-emu','12192000,6858000','--validate-bullet-geometry','--validate-heading-fit',...nativeTables.flatMap(n=>['--require-native-table-slide',String(n)])],
 requiredNativeTableOwnerSlides:nativeTables,
 requiredNativeChartOwnerSlides:[16],
 materializeLiteralChartWorkbooks:true,
 fontPolicy:{basis:'design',families:[font]},
 verifyArtifactToolImport:true,
 receiptPath:path.join(build,'presentation-validation-'+revision+'.json'),
});
const imported=await PresentationFile.importPptx(await FileBlob.load(checked));
for(let i=0;i<imported.slides.items.length;i++){
 const slide=imported.slides.items[i];
 const png=await imported.export({slide,format:'png',scale:1.5});
 await fs.writeFile(path.join(preview,'slide-'+String(i+1).padStart(2,'0')+'.png'),new Uint8Array(await png.arrayBuffer()));
 const layout=await slide.export({format:'layout'});
 await fs.writeFile(path.join(preview,'slide-'+String(i+1).padStart(2,'0')+'.json'),await layout.text());
}
await fs.writeFile(path.join(build,'presentation-copy.txt'),allCopy.join('\n'));
console.log(JSON.stringify({slides:imported.slides.items.length,nativeTables,checked,preview}));
