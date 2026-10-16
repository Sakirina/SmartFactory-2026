// Record the running platform. Inputs go to the actual protocol simulator;
// every claimed action is checked against its state and a rendered metric.
import fs from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';
import { spawn } from 'node:child_process';

const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'..');
const require=createRequire(path.join(root,'Frontends/package.json'));
const {chromium}=require('@playwright/test');
const state=process.env.SF_STATE_DIRECTORY||path.join(root,'.local');
const privateValues=JSON.parse(await fs.readFile(path.join(state,'development-credentials.json'),'utf8'));
const cloud=process.env.SF_DEMO_CLOUD||'http://127.0.0.1:8090';
const edge=process.env.SF_DEMO_EDGE||'http://127.0.0.1:8091';
const simulator=process.env.SF_DEMO_SIMULATOR||'http://127.0.0.1:18083';
const ffmpeg=process.env.SF_FFMPEG||'ffmpeg';
const build=path.join(root,'.local/materials-build/video');
await fs.mkdir(build,{recursive:true});
await fs.mkdir(path.join(root,'deliverables'),{recursive:true});
const api=async(base,route,value,token='')=>{
  const response=await fetch(base+route,{method:value===undefined?'GET':'POST',body:value===undefined?undefined:JSON.stringify(value),headers:{'Content-Type':'application/json',Authorization:'Bearer '+token},signal:AbortSignal.timeout(20000)});
  const data=await response.json();if(!response.ok)throw new Error(`${route}: ${response.status} ${data.error||''}`);return data;
};
const login=async(base,user='admin')=>(await api(base,'/api/sf/v1/login',{login:user,password:privateValues.password})).token;
const ct=await login(cloud),et=await login(edge);
const definitions=[
  ['climate','温湿度监测与自动调节','climate-1',[['temperature','温度'],['humidity','湿度'],['fan','通风'],['heater','加热'],['humidifier','加湿'],['dehumidifier','除湿']]],
  ['light','红外感应照明','light-1',[['presence','人员感应'],['light','照明']]],
  ['gas','危气监测与排风','gas-1',[['smoke','烟雾'],['combustible','可燃气'],['co','CO'],['extractor','排风']]],
  ['agv','AGV 避障与确认恢复','agv-1',[['distance','障碍距离'],['stopped','停车状态']]],
  ['counter','货物事件去重与累计','counter-1',[['total','设备累计值'],['goods-count.total','平台累计值']]],
];
let existing=await api(cloud,'/api/sf/v1/dashboards',undefined,ct);
for(const [id,title,device,metrics] of definitions){
  const previous=existing.find(d=>d.id==='demo-'+id);
  const dashboard={id:'demo-'+id,title,group_id:'factory',version:previous?.version||0,device_ids:[device],keys:metrics.map(m=>m[0]),metrics:metrics.map(([key,label])=>({device_id:device,key,label})),window_ms:60000,refresh_ms:1000,show_sources:true,show_alarms:true};
  await api(cloud,'/api/sf/v1/dashboards',{dashboard,expected_version:dashboard.version},ct);
}
const browser=await chromium.launch({headless:true,executablePath:process.env.SF_BROWSER_EXECUTABLE||(process.platform==='darwin'?'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome':'/usr/bin/chromium')});
const context=await browser.newContext({viewport:{width:1600,height:1000},locale:'zh-CN',timezoneId:'Asia/Shanghai'});
await context.addInitScript(({cloud,edge,ct,et})=>{
  if(location.origin===cloud)sessionStorage.setItem('smartfactory.session.'+location.host,ct);
  if(location.origin===edge)sessionStorage.setItem('smartfactory.session.'+location.host,et);
},{cloud,edge,ct,et});
const page=await context.newPage();
const errors=[];page.on('pageerror',error=>errors.push(error.message));
await page.goto(cloud);await page.getByRole('heading',{name:'工厂运行概览',exact:true}).waitFor();
await page.waitForTimeout(1600);
const shots=path.join(root,'docs/evidence/screenshots');await fs.mkdir(shots,{recursive:true});
await page.setViewportSize({width:1440,height:900});
await page.screenshot({path:path.join(shots,'cloud-1440.png'),fullPage:true});
await page.getByRole('button',{name:'数据分析',exact:true}).click();
await page.getByRole('button',{name:'编辑与评审',exact:true}).first().click();
await page.locator('.react-flow__node').first().waitFor();
await page.waitForTimeout(1200);
await page.screenshot({path:path.join(shots,'editor-1440.png'),fullPage:true});
await page.getByRole('button',{name:'关闭',exact:true}).click();
const mobile=await context.newPage();await mobile.setViewportSize({width:390,height:844});
await mobile.goto(edge+'/edge');await mobile.getByRole('heading',{name:'现场运行概览',exact:true}).waitFor();
await mobile.waitForTimeout(1500);
await mobile.screenshot({path:path.join(shots,'edge-390.png'),fullPage:true});
await mobile.screenshot({path:path.join(shots,'edge-390-viewport.png'),fullPage:false});
await mobile.close();
await page.goto(cloud+'/screen');await page.getByRole('heading',{name:'示例工厂 · 生产安全运行',exact:true}).waitFor();
await page.setViewportSize({width:1920,height:1080});await page.waitForTimeout(1500);
await page.screenshot({path:path.join(shots,'screen-1920.png'),fullPage:true});
await page.setViewportSize({width:1600,height:1000});await page.goto(cloud);await page.waitForTimeout(1000);

const raw=path.join(build,'browser-raw.mp4');
const processVideo=spawn(ffmpeg,['-hide_banner','-loglevel','error','-y','-f','image2pipe','-vcodec','mjpeg','-framerate','10','-i','pipe:0','-an','-c:v','libx264','-preset','veryfast','-crf','20','-pix_fmt','yuv420p','-movflags','+faststart',raw],{stdio:['pipe','ignore','pipe']});
let ffmpegError='';processVideo.stderr.on('data',chunk=>ffmpegError+=chunk);
const videoDone=new Promise((resolve,reject)=>{processVideo.on('exit',code=>code===0?resolve():reject(new Error(ffmpegError)));processVideo.on('error',reject);});
let frame=await page.screenshot({type:'jpeg',quality:85}),frames=0;
const cdp=await context.newCDPSession(page);
cdp.on('Page.screencastFrame',event=>{frame=Buffer.from(event.data,'base64');void cdp.send('Page.screencastFrameAck',{sessionId:event.sessionId});});
await cdp.send('Page.startScreencast',{format:'jpeg',quality:85,maxWidth:1600,maxHeight:1000,everyNthFrame:1});
const timer=setInterval(()=>{if(processVideo.stdin.writableLength<8*1024*1024){processVideo.stdin.write(frame);frames++;}},100);
const captions=[];
const caption=value=>{if(captions.length)captions.at(-1).end=frames*100;captions.push({start:frames*100,text:value});console.log(value);};
const hold=ms=>page.waitForTimeout(ms);
const wait=async(fn,label,timeout=30000)=>{const until=Date.now()+timeout;while(Date.now()<until){if(await fn())return;await hold(200);}throw new Error(label);};
const report={started_ms:Date.now(),source:'actual Chromium application frames; protocol simulator inputs; state and DOM checks',native_services:process.env.SF_DEMO_NATIVE_STATE||'unspecified',steps:[],screenshots:Object.keys({cloud:1,edge:1,screen:1})};
const show=async id=>{
  if(new URL(page.url()).origin!==cloud)await page.goto(cloud+'/screen');
  await page.evaluate(id=>localStorage.setItem('sf.screen.selected','demo-'+id),id);
  await page.goto(cloud+'/screen');await page.getByRole('heading',{name:definitions.find(d=>d[0]===id)[1],exact:true}).waitFor();await hold(1200);
};
const set=async(device,values)=>api(simulator,'/state',{device_id:device,values});
const current=async(device,key)=>(await api(simulator,'/state')).devices[device][key];
const check=async(device,key,value,label)=>{
  await wait(async()=>await current(device,key)===value,device+'/'+key);
  const shown=typeof value==='boolean'?(value?'开启':'关闭'):Number(value).toLocaleString('zh-CN');
  await wait(async()=>((await page.locator('.indicator-button').filter({has:page.locator('span',{hasText:label})}).first().locator('strong').textContent())||'').startsWith(shown),'rendered '+label);
  report.steps.push({device,key,value,verified_ms:Date.now()});
};
let completed=false;
try{
  caption('SmartFactory 实际运行演示\n云端控制台、边缘现场页面与管理大屏共用业务数据');await hold(6500);
  await page.getByRole('button',{name:'数据分析',exact:true}).click();await page.getByRole('button',{name:'编辑与评审',exact:true}).first().click();
  caption('分析、告警和联动预案共用编排与评审流程\n当前展示温度一分钟平均值的可编辑定义');await hold(6500);
  await page.getByRole('button',{name:'关闭',exact:true}).click();
  await set('climate-1',{temperature:26,humidity:55});await show('climate');
  caption('温湿度监控：升高温度后，边缘规则启动通风\n页面中的读数和执行状态来自实际协议采集');await hold(3000);
  await set('climate-1',{temperature:34});await check('climate-1','fan',true,'通风');await hold(5000);
  caption('温度恢复至回差范围后，通风关闭');await set('climate-1',{temperature:26});await check('climate-1','fan',false,'通风');await hold(4500);
  caption('湿度升高时启动除湿；恢复后关闭除湿');await set('climate-1',{humidity:80});await check('climate-1','dehumidifier',true,'除湿');await hold(4000);
  await set('climate-1',{humidity:55});await check('climate-1','dehumidifier',false,'除湿');await hold(3500);
  await show('light');caption('红外感应照明：检测到人员时点亮\n人员离开后，按预案延时关闭');await set('light-1',{presence:true});await check('light-1','light',true,'照明');await hold(4000);
  await set('light-1',{presence:false});await check('light-1','light',false,'照明');await hold(4000);
  await show('gas');
  for(const [key,value,name] of [['smoke',10,'烟雾'],['combustible',40,'可燃气'],['co',60,'CO']]){
    await set('gas-1',{smoke:0,combustible:0,co:0,extractor:false});await hold(1600);
    caption(`危气监测：${name} 超限，触发告警和排风预案`);await set('gas-1',{[key]:value});await check('gas-1','extractor',true,'排风');await hold(4500);
  }
  await set('gas-1',{smoke:0,combustible:0,co:0});
  await show('agv');caption('AGV 避障：距离低于停止阈值，现场自动停车');await set('agv-1',{distance:25});await check('agv-1','stopped',true,'停车状态');await hold(4500);
  caption('距离恢复后，工程师与领导分别确认恢复请求\n边缘在执行前再次检查当前条件');await set('agv-1',{distance:150});await hold(1500);
  const engineer=await login(edge,'engineer'),leader=await login(edge,'leader');
  const execution=await api(edge,'/api/sf/v1/executions',{definition_id:'agv-resume',params:{},downlink_id:'video-agv-'+Date.now()},engineer);
  await api(edge,'/api/sf/v1/executions/'+execution.downlink_id+'/approve',{role:'engineer'},engineer);
  await api(edge,'/api/sf/v1/executions/'+execution.downlink_id+'/approve',{role:'leader'},leader);
  await api(edge,'/api/sf/v1/executions/'+execution.downlink_id+'/dispatch',{},engineer);
  await check('agv-1','stopped',false,'停车状态');await hold(5000);
  await page.goto(edge+'/edge#executions');await page.getByRole('heading',{name:'审批与执行',exact:true}).waitFor();await hold(5000);
  await show('counter');const before=await current('counter-1','total');
  caption(`货物计数：输入 100 个有效事件，并注入重复投递\n设备与平台累计值应从 ${before} 增加到 ${before+100}`);
  await api(simulator,'/pulses',{count:100,duplicate_every:3});await check('counter-1','total',before+100,'设备累计值');
  await wait(async()=>((await page.locator('.indicator-button').filter({has:page.locator('span',{hasText:'平台累计值'})}).locator('strong').textContent())||'').startsWith(Number(before+100).toLocaleString('zh-CN')),'deduplicated platform count',60000);await hold(6000);
  report.steps.push({device:'counter-1',key:'goods-count.total',value:before+100,verified_ms:Date.now()});
  await page.locator('.indicator-button').last().click();caption('从指标继续查看观测记录、质量与修订信息');await hold(5000);
  await page.getByRole('button',{name:'关闭',exact:true}).click();
  caption('本片记录开发环境中的实际页面与设备反馈\nUOS 和连续 24 小时离线验收将随目标环境完成');await hold(6000);
  if(errors.length)throw new Error('browser errors: '+errors.join('; '));
  completed=true;
}finally{
  clearInterval(timer);captions.at(-1).end=frames*100;
  await cdp.send('Page.stopScreencast');processVideo.stdin.end();await videoDone;
  await browser.close();
  report.finished_ms=Date.now();report.duration_ms=frames*100;report.passed=completed;
  await fs.writeFile(path.join(build,'recording-report.json'),JSON.stringify(report,null,2)+'\n');
}
const time=ms=>`${String(Math.floor(ms/3600000)).padStart(2,'0')}:${String(Math.floor(ms/60000)%60).padStart(2,'0')}:${String(Math.floor(ms/1000)%60).padStart(2,'0')},${String(ms%1000).padStart(3,'0')}`;
const srt=captions.map((c,i)=>`${i+1}\n${time(c.start)} --> ${time(c.end)}\n${c.text}\n`).join('\n');
const subtitle=path.join(root,'deliverables/SmartFactory演示字幕.srt');await fs.writeFile(subtitle,srt);
const result=path.join(root,'deliverables/SmartFactory五场景演示.mp4');
const filter=`subtitles=filename='${subtitle.replaceAll(':','\\:')}':force_style='FontName=Heiti SC,FontSize=12,Outline=1,Shadow=0,BorderStyle=3,BackColour=&H88000000,MarginV=20'`;
await new Promise((resolve,reject)=>{const child=spawn(ffmpeg,['-hide_banner','-loglevel','error','-y','-i',raw,'-vf',filter,'-an','-c:v','libx264','-preset','veryfast','-crf','20','-pix_fmt','yuv420p','-movflags','+faststart',result],{stdio:['ignore','ignore','inherit']});child.on('error',reject);child.on('exit',code=>code===0?resolve():reject(new Error('subtitle render failed')));});
await fs.copyFile(path.join(build,'recording-report.json'),path.join(root,'docs/evidence/demo-video.json'));
console.log(JSON.stringify({video:result,subtitle,duration_seconds:frames/10,passed:true}));
