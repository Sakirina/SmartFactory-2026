import fs from 'node:fs/promises';
import path from 'node:path';
import { Presentation, PresentationFile } from '@oai/artifact-tool';
import { GlobalFonts } from '@napi-rs/canvas';
import { finalizePresentation, applyPresentationChartFont } from '/home/developer/.codex/plugins/cache/openai-primary-runtime/presentations/26.904.11930/skills/presentations/container_tools/artifact_tool_utils.mjs';

const root='/path/to/SmartFactory-2026';
const temp=path.join(root,'.local/materials-build');
const skill='/home/developer/.codex/plugins/cache/openai-primary-runtime/presentations/26.904.11930/skills/presentations';
const python='/home/developer/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3';
GlobalFonts.registerFromPath('/System/Library/Fonts/STHeiti Light.ttc');
GlobalFonts.registerFromPath('/System/Library/Fonts/STHeiti Medium.ttc');
const font='Heiti SC';
const deck=Presentation.create({slideSize:{width:1280,height:720}});
const ink='#102B3B',muted='#4B646E',teal='#137F77',paper='#FCFCF9';
function text(slide,value,x,y,w,h,size=28,color=ink,bold=false){
  const shape=slide.shapes.add({geometry:'textbox',position:{left:x,top:y,width:w,height:h},fill:'none',line:{fill:'none',width:0}});
  shape.text=value;shape.text.style={typeface:font,fontSize:size,color,bold,autoFit:'none'};
  return shape;
}
function slide(title,notes='',dark=false){
  const s=deck.slides.add();s.background.fill=dark?ink:paper;
  text(s,title,64,42,1152,84,44,dark?'#FFFFFF':ink,true);
  s.speakerNotes.textFrame.setText(notes);
  return s;
}
async function image(s,name,x,y,w,h,crop){
  s.images.add({blob:new Uint8Array(await fs.readFile(path.join(root,'docs/evidence/screenshots',name))),contentType:'image/png',alt:name,fit:'contain',position:{left:x,top:y,width:w,height:h},...(crop?{crop}:{})});
}
function table(s,values,widths,y=170,height=410){
  const t=s.tables.add({rows:values.length,columns:values[0].length,left:64,top:y,width:1152,height,columnWidths:widths,values});
  t.borders.assign({style:'solid',fill:'#CCD7DA',width:1});
  t.cells.block({row:0,column:0,rowCount:values.length,columnCount:values[0].length}).assign({textStyle:{typeface:font,fontSize:26,color:ink},margins:{top:13,bottom:13,left:15,right:15},anchor:'center'});
  t.cells.block({row:0,column:0,rowCount:1,columnCount:values[0].length}).assign({fill:'#E4EFED',textStyle:{typeface:font,fontSize:26,color:ink,bold:true}});
  return t;
}

let s=deck.slides.add();s.background.fill=ink;
text(s,'SmartFactory',72,132,1136,98,76,'#FFFFFF',true);
text(s,'工厂监测与现场联动平台',72,260,1110,72,48,'#FFFFFF');
text(s,'产品介绍与阶段验证结果',76,366,1090,52,30,'#A8D4CF');
text(s,'2026 年 9 月 20 日预稿',76,594,1050,42,24,'#C4D5DB');
s.speakerNotes.textFrame.setText('产品介绍预稿。当前五场景及阶段测量已完成，UOS 全系统及实际 24 小时离线、容量验收待目标环境准备后执行。事实依据为 docs/implementation/test-report.md 与 requirements.md。');

s=slide('五个场景中的监测与控制','来源：docs/evidence/five-scenes.json；docs/evidence/counter-reliability.json。五场景实际协议状态转换共 16 项通过。计数可靠性另有新增一万事件与重启、断连检查。');
table(s,[['场景','判断与动作','反馈'],['温湿度','阈值、回差与联锁后调节','读数恢复、执行回执'],['感应照明','有人点亮，无人延时关闭','状态变化、抖动处理'],['危气监测','烟雾、可燃气、CO 触发排风','跨节点步骤、降级原因'],['AGV 避障','距离不足停车，确认后恢复','停车状态、条件复核'],['货物计数','按事件身份去重并累计','重复、重启后的计数']],[225,555,372],156,440);
text(s,'控制命令会改变模拟设备的后续状态，云端与现场页面展示反馈。',66,632,1140,42,25,muted);

s=slide('云端与现场的系统架构','来源：docs/implementation/architecture.md；Platform/internal/app、cloudsync、coordination 与 thingsboard。业务云边连接使用 mTLS，DataTransfer 与本机业务桥使用本地 gRPC。每个节点具有独立业务数据库。');
const cloudBox=s.shapes.add({geometry:'rect',name:'云端平台',position:{left:64,top:153,width:748,height:135},fill:'#E4EFED',line:{fill:'#86AEA9',width:1.4}});
text(s,'云端平台 · mTLS 同步',85,168,697,44,32,teal,true);
text(s,'ThingsBoard CE  ·  Go 业务服务',85,218,697,43,27);
const configBox=s.shapes.add({geometry:'rect',name:'配置中心',position:{left:866,top:153,width:350,height:135},fill:'#EDF1F4',line:{fill:'#9CACB5',width:1.4}});
text(s,'独立配置中心',885,167,308,43,30,teal,true);
text(s,'参数 Schema、版本与凭据',885,224,308,43,23);
s.shapes.connect(configBox,cloudBox,{fromSide:'left',toSide:'right',kind:'straight',line:{fill:muted,width:2},head:{type:'arrow'}});
const edgeBoxes=[];
for(let index=0;index<3;index++){
 const x=64+index*401;
 const box=s.shapes.add({geometry:'rect',name:'边缘节点'+(index+1),position:{left:x,top:393,width:350,height:190},fill:'#FFFFFF',line:{fill:'#8EA8B4',width:1.5}});
 edgeBoxes.push(box);
 text(s,'边缘节点 '+(index+1),x+18,410,312,45,30,teal,true);
 text(s,'ThingsBoard Edge / Go 服务',x+18,467,312,38,20);
 text(s,'DataTransfer / PostgreSQL',x+18,504,312,38,22);
 text(s,'本地页面、授权与设备命令',x+18,541,312,35,22);
 s.shapes.connect(cloudBox,box,{fromSide:'bottom',toSide:'top',kind:'elbow',line:{fill:'#86AEA9',width:2},head:{type:'arrow'}});
}
const siteBox=s.shapes.add({geometry:'rect',name:'现场协调副本',position:{left:287,top:640,width:706,height:53},fill:ink,line:{fill:ink,width:0}});
text(s,'三个 NATS JetStream 副本：状态、执行权与进度',303,653,675,34,24,'#FFFFFF');
for(const box of edgeBoxes)s.shapes.connect(box,siteBox,{fromSide:'bottom',toSide:'top',kind:'elbow',line:{fill:'#86AEA9',width:1.7},head:{type:'arrow'}});

s=slide('云端控制台','实际截图：docs/evidence/screenshots/cloud-1440.png。浏览器功能检查：docs/evidence/frontend-results.json。画面是开发环境的实时页面，数据数量对应截图时刻。');
await image(s,'cloud-1440.png',58,145,770,510);
text(s,'同一入口查看工厂状态',870,181,344,92,31,teal,true);
text(s,'设备管理、监控与编排页面共用业务 API。',870,302,344,138,28);
text(s,'页面显示质量、来源状态和待投递数量。',870,475,344,137,28);

s=slide('分析、告警与联动预案','实际截图：docs/evidence/screenshots/editor-1440.png。实现：Frontends/src；Platform/internal/definition 与 engine。发布检查定义种类、类型、依赖及版本，人工从评审页面发布。');
await image(s,'editor-1440.png',60,142,930,488);
text(s,'保存草稿',1020,185,198,52,29,teal,true);
text(s,'校验类型与依赖',984,280,232,92,26);
text(s,'样本模拟\n查看差异\n评审与发布',984,415,232,143,26);
text(s,'示例：温度读数的一分钟平均值，结果持久保存并加入数据目录。',64,642,1148,42,25,muted);

s=slide('现场移动页面','实际截图：docs/evidence/screenshots/edge-390-viewport.png，展示完整移动视口。检查尺寸为 390×844。静态资源随边缘部署，本地使用已同步的有效权限包。');
await image(s,'edge-390-viewport.png',160,145,248,518);
text(s,'在设备现场完成批准与检查',503,191,685,72,34,teal,true);
text(s,'现场人员核对物理设备、协议地址与采集配置，再批准启用。',503,310,685,106,30);
text(s,'断云期间，页面读取本地数据与既有权限，操作已发布预案并保存回执。',503,448,685,139,30);

s=slide('管理大屏与来源状态','实际截图：docs/evidence/screenshots/screen-1920.png；实现：Frontends。大屏使用同一业务数据，可配置指标、趋势及进一步查询的范围。当前截图中的未知值与来源状态按实际页面保留。',true);
await image(s,'screen-1920.png',62,142,1156,496);
text(s,'指标与趋势附带质量、来源及补传状态，支持进一步查看详情。',64,653,1150,38,25,'#CEE0E3');

s=slide('控制审批与现场责任','来源：Platform/internal/control 与 identity；docs/implementation/architecture.md。业务强制需要两位不同工程师和一位领导，安全强制还需要指定现场安全员在本地以二次认证确认。各步骤前再次检查物理联锁。');
table(s,[['操作','默认要求'],['普通即时控制','一名工程师和一名领导'],['业务强制执行','两名工程师和一名领导'],['安全强制执行','上述会签，加指定现场安全员本地二次认证'],['物理安全联锁','每个设备步骤前检查当前联锁状态']],[335,817],159,348);
text(s,'5 分钟',68,553,350,54,37,teal,true);
text(s,'人工确认有效期',68,620,370,40,26,muted);
text(s,'10 秒',520,553,430,54,37,teal,true);
text(s,'正式下发后的启动期限',520,620,690,40,26,muted);

s=slide('数据质量与历史修订','来源：Platform/internal/store、engine；docs/implementation/architecture.md；docs/evidence/archive-postgres-v2.json。默认 GOOD 参与计算，原始质量和原因保存。补算按照历史规则及资产版本计算，不触发设备控制。');
text(s,'完整原始记录',66,162,510,60,34,teal,true);
text(s,'值、精度、协议质量、时间来源，以及设备和资产版本随观测保存。',66,245,510,146,30);
text(s,'较早观测分块压缩，热数据和归档由同一查询入口读取。',66,428,510,146,30);
text(s,'可检查的修订依据',670,162,545,60,34,teal,true);
text(s,'补传按相应历史规则和资产版本重算，保留旧值、新值与原因。',670,245,545,146,30);
text(s,'页面显示质量排除、已知缺口、来源状态与未完成补算。',670,428,545,146,30);

s=slide('页面助手与外部 MCP','来源：Platform/internal/ai、mcp；docs/evidence/mcp-client.json、frontend-results.json。固定模型模拟器检验流式输出、工具调用、三类定义草稿及错误分支。AI 不能读取秘密、直接发布或操作设备。');
text(s,'查询按照数据目录逐步展开',66,160,1145,66,35,teal,true);
text(s,'1. 发现目录与字段结构\n2. 选择资源及时间范围\n3. 查询结果并查看质量信息',68,266,538,266,30);
text(s,'草稿协作',701,259,493,60,34,teal,true);
text(s,'创建、修改、校验和差异比较使用与页面相同的定义对象。正式发布由人员在评审页面完成。',701,351,493,192,30);
text(s,'服务端按身份检查资源权限，模型端点、名称、密钥和超时可配置。',67,634,1142,44,25,muted);

s=slide('500 台设备持续采集测量','来源：docs/evidence/capacity-disk-60m.json。500 台设备为 200 Modbus、200 MQTT、100 OPC-UA。3600.0127 秒保存 18654020 条单测点消息，输入与保存一致。最大 RSS 61341696 字节，1000 次控制 p99 18.584ms。完整时长对应报告内二进制 SHA-256，后续批量和归档变更另有回归。');
const chart=s.charts.add('bar',{position:{left:67,top:174,width:746,height:320},categories:['验收门槛','实测结果'],series:[{name:'单测点消息每秒',values:[5000,5181.65],fill:teal,dataLabelOverrides:[{idx:0,text:'5000',textStyle:{typeface:font,fontSize:24}},{idx:1,text:'5181.65',textStyle:{typeface:font,fontSize:24}}]}],barOptions:{direction:'bar',grouping:'clustered'},hasLegend:false,xAxis:{textStyle:{typeface:font,fontSize:24}},yAxis:{numberFormatCode:'0',min:0,max:6000,majorUnit:2000,textStyle:{typeface:font,fontSize:24}},dataLabels:{showValue:true,position:'outEnd',textStyle:{typeface:font,fontSize:24}}});
applyPresentationChartFont(chart,{fontFamily:font});
text(s,'单测点消息 / 秒',68,140,730,40,25,muted);
text(s,'58.5 MiB',880,199,328,74,46,teal,true);
text(s,'DataTransfer 最大 RSS',880,283,338,73,26,muted);
text(s,'18.584 ms',880,397,338,73,43,teal,true);
text(s,'1000 次控制的 p99',880,482,338,62,26,muted);
text(s,'60 分钟，共 18654020 条单测点消息，输入与保存数量一致。',67,572,1142,51,29);
text(s,'开发环境实测；完整时长对应报告中记录的二进制版本。',67,645,1142,39,24,muted);

s=slide('持久消息与业务查询测量','来源：docs/evidence/A06-durable-100000.json；api-postgres-archive-30s.json。A06 是实际 DataTransfer 进程、gRPC 与独立业务 SQLite 消费者检查。A15 为 PostgreSQL 归档与热数据混合查询，预置30天原始样本和三年汇总。API/SSE延迟与目标浏览器绘制延迟分别验收。');
table(s,[['检查','规模与结果'],['持久消息','100000 个事件，14285 次丢确认，3 次进程重启'],['确认与去重','已确认缺失 0，重复业务效果 0'],['实时与历史 API','20 个 SSE 订阅、10 个查询客户端，持续 30 秒'],['历史查询延迟','每次最多 20 序列、每序列 2000 点，p95 1311.70 ms'],['实时订阅延迟','14980 个实时点一致，SSE p95 1045 ms']],[290,862],154,441);
text(s,'目标 UOS 环境将继续验证持续页面绘制延迟与长时间离线恢复。',65,635,1148,43,25,muted);

s=slide('目标环境与后续验收','来源：用户确认的完整交付计划；docs/implementation/requirements.md、test-report.md。UOS VM 尚待创建。A08、A09 和 A12 实际24小时检查及A01完整部署待执行。完整原始保留的总容量包含投影、修订、发件箱、审计和备份，需在24小时实测后调整虚拟机容量。');
text(s,'统信 UOS Server V20 x86_64',66,163,1145,70,37,teal,true);
text(s,'初始配置为 8 vCPU、16GB 内存、220GB 磁盘，容量按完整测量调整。',67,263,1138,95,29);
text(s,'500 设备单节点及三节点连续断云 24 小时，检查本地业务、权限和审计。',67,383,1138,95,29);
text(s,'恢复链路限定为 100Mbps、20ms，测量六小时内的补传与补算。',67,507,1138,90,29);
text(s,'当前阶段结果来自开发环境，最终代码验收以全部必验项目通过为条件。',67,639,1138,43,24,muted);

s=slide('交付内容与时间安排','来源：用户确认的完整交付计划及项目 README。10月20日来自往年经验，按学校和发榜单位后续通知调整。开源固定版本为 ThingsBoard CE v4.3.1.5、Edge 4.3.1.1EDGE。参赛者署名及声明函手续由参赛者完成。');
table(s,[['日期','交付安排'],['10 月 7 日前','产品介绍与 PPT 预稿'],['10 月 18 日','完整仿真验收目标'],['10 月 19 日','源码、部署包、设计与开发文档、测试报告及演示视频'],['10 月 20 日前','暂定最终材料准备时间，随正式通知调整']],[270,882],158,346);
text(s,'上游基础与自研内容分别说明',66,549,1140,54,32,teal,true);
text(s,'源码包保留许可证、组件清单、修改说明、公共契约及可复现验收工具。',66,620,1140,50,26,muted);

await fs.mkdir(path.join(temp,'deck-review'),{recursive:true});
const candidate=path.join(temp,'candidate-03.pptx');
await (await PresentationFile.exportPptx(deck)).save(candidate);
const finalPath=path.join(root,'deliverables/SmartFactory产品介绍预稿-r3.pptx');
const result=await finalizePresentation({workspaceDir:root,candidatePath:candidate,finalPath,pythonExecutable:python,
  integrityValidatorPath:path.join(skill,'container_tools/inspect_presentation_package_integrity.py'),
  layoutValidatorPath:path.join(skill,'container_tools/inspect_presentation_layout_geometry.py'),
  layoutArgs:['--expected-slide-size-emu','12192000,6858000','--validate-bullet-geometry','--validate-heading-fit',...[2,8,12,14].flatMap(n=>['--require-native-table-slide',String(n)])],
  requiredNativeTableOwnerSlides:[2,8,12,14],requiredNativeChartOwnerSlides:[11],materializeLiteralChartWorkbooks:true,
  fontPolicy:{basis:'design',families:[font]},verifyArtifactToolImport:true,receiptPath:path.join(temp,'deck-validation-03.json')});
for(let i=0;i<deck.slides.items.length;i++){
 const rendered=await deck.export({slide:deck.slides.items[i],format:'png',scale:1});
 await fs.writeFile(path.join(temp,'deck-review',`slide-${i+1}.png`),new Uint8Array(await rendered.arrayBuffer()));
}
const architecture=await deck.export({slide:deck.slides.items[2],format:'png',scale:2});
await fs.writeFile(path.join(root,'docs/evidence/screenshots/architecture.png'),new Uint8Array(await architecture.arrayBuffer()));
console.log(JSON.stringify({output:finalPath,slides:deck.slides.items.length,validation:result}));
