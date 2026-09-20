// Attach to sf-api-benchmark. Observe the application's own SSE response and
// its rendered indicator, then measure two animation frames after a DOM change.
import fs from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import {fileURLToPath} from 'node:url';
import {createRequire} from 'node:module';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'..');
const require=createRequire(path.join(root,'Frontends/package.json'));
const {chromium}=require('@playwright/test');
const directory=process.argv[2];
if(!directory)throw new Error('Usage: node scripts/check-browser-latency.mjs <sf-api-benchmark-directory> [seconds=30]');
const seconds=Number(process.argv[3]||30);if(seconds<30||seconds>600)throw new Error('duration must be 30..600 seconds');
const clientCount=Number(process.env.SF_BROWSER_CLIENTS||20);if(!Number.isInteger(clientCount)||clientCount<1||clientCount>20)throw new Error('SF_BROWSER_CLIENTS must be 1..20');
const connection=JSON.parse(await fs.readFile(path.join(directory,'browser-connection.json'),'utf8'));
const response=await fetch(connection.base_url+'/api/sf/v1/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({login:connection.login,password:connection.password})});
if(!response.ok)throw new Error('fixture login failed');const {token}=await response.json();
const browser=await chromium.launch({headless:true,executablePath:process.env.SF_BROWSER_EXECUTABLE||(process.platform==='darwin'?'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome':'/usr/bin/chromium'),args:['--disable-background-timer-throttling','--disable-renderer-backgrounding']});
const initializePage=({token})=>{
  sessionStorage.setItem('smartfactory.session.'+location.host,token);
  const series=new URL(location.href).searchParams.get('series');if(series)localStorage.setItem('sf.screen.selected',series);
  const sources=new Map(),seen=new Set();window.__sfPaint={enabled:false,samples:[],failures:[],frames:0};
  const original=window.fetch.bind(window);
  window.fetch=async(...args)=>{
    const response=await original(...args);
    if(String(args[0]).includes('/api/sf/v1/events')){
      void (async()=>{
        const reader=response.clone().body.getReader(),decoder=new TextDecoder();let pending='';
        for(;;){const chunk=await reader.read();if(chunk.done)break;pending+=decoder.decode(chunk.value,{stream:true});const frames=pending.split('\n\n');pending=frames.pop();
          for(const frame of frames){const line=frame.split('\n').find(l=>l.startsWith('data: '));if(!line)continue;const result=JSON.parse(line.slice(6));window.__sfPaint.frames++;
            for(const point of result.points||[]){if(String(point.id).startsWith('live:'))sources.set(String(point.value),point);}
            while(sources.size>1000)sources.delete(sources.keys().next().value);
          }
        }
      })().catch(error=>window.__sfPaint.failures.push(error.message));
    }
    return response;
  };
  let scheduled=false;
  new MutationObserver(()=>{
    if(scheduled||!window.__sfPaint.enabled)return;scheduled=true;
    requestAnimationFrame(()=>requestAnimationFrame(()=>{
      scheduled=false;const text=document.querySelector('.indicator-button strong')?.textContent?.replaceAll(',','').trim();const point=sources.get(text);
      if(point&&!seen.has(point.id)){seen.add(point.id);window.__sfPaint.samples.push({id:point.id,observed_ms:point.observed_ms,paint_ms:Date.now(),latency_ms:Date.now()-point.observed_ms});}
    }));
  }).observe(document,{childList:true,subtree:true,characterData:true});
};
const pages=[],errors=[],requests=[];
try{
  // Limit startup concurrency; the measurement begins only after all 20 pages render.
  for(let start=0;start<clientCount;start+=4){
    await Promise.all(Array.from({length:Math.min(4,clientCount-start)},async(_,offset)=>{const index=start+offset,context=await browser.newContext({viewport:{width:1440,height:900},locale:'zh-CN'});await context.addInitScript(initializePage,{token});const page=await context.newPage();pages[index]=page;page.on('pageerror',e=>errors.push(e.message));page.on('response',response=>{if(response.status()>=400)requests.push({index,url:response.url(),status:response.status()});});await page.goto(connection.base_url+'/screen?series=series-'+String(index).padStart(2,'0'));await page.locator('.indicator-button strong').waitFor();}));
  }
  await pages[0].waitForTimeout(3000);
  await Promise.all(pages.map(page=>page.evaluate(()=>{window.__sfPaint.enabled=true;window.__sfPaint.samples=[];})));
  const started=Date.now();console.log(`${clientCount} application pages ready; measuring source timestamp to DOM update plus two animation frames.`);
  await pages[0].waitForTimeout(seconds*1000);
  const clients=await Promise.all(pages.map((page,index)=>page.evaluate(index=>({index,...window.__sfPaint}),index)));
  const values=clients.flatMap(c=>c.samples.map(s=>s.latency_ms)).sort((a,b)=>a-b);
  const percentile=q=>values[Math.min(values.length-1,Math.floor(values.length*q))]??null;
  const report={status:'measured',environment:`${os.platform()}/${os.arch()}`,browser:browser.version(),concurrent_browser_pages:clientCount,independent_browser_contexts:clientCount,additional_api_streams:20,measurement:'source observation timestamp to actual indicator DOM update plus two requestAnimationFrame callbacks',started_ms:started,duration_seconds:seconds,sample_count:values.length,p50_ms:percentile(.5),p95_ms:percentile(.95),p99_ms:percentile(.99),maximum_ms:values.at(-1),clients:clients.map(c=>({index:c.index,samples:c.samples.length,frames:c.frames,failures:c.failures})),browser_errors:errors,failed_requests:requests};
  report.passed=clientCount===20&&values.length>=20*10&&report.p95_ms<=2000&&errors.length===0&&clients.every(c=>c.samples.length>=10&&c.failures.length===0);
  await fs.writeFile(path.join(directory,'browser-report.json'),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify(report));
  if(!report.passed)process.exitCode=1;
}catch(error){
  const diagnostics=[];
  for(let index=0;index<pages.length;index++){const page=pages[index];if(!page)continue;diagnostics.push({index,url:page.url(),body:await page.locator('body').innerText().catch(()=>''),paint:await page.evaluate(()=>window.__sfPaint).catch(()=>null)});if(index<4)await page.screenshot({path:path.join(directory,`browser-failure-${index}.png`),fullPage:true}).catch(()=>{});}
  await fs.writeFile(path.join(directory,'browser-report.json'),JSON.stringify({status:'failed',passed:false,error:String(error),browser_errors:errors,failed_requests:requests,pages:diagnostics},null,2)+'\n');
  throw error;
}finally{await browser.close();}
