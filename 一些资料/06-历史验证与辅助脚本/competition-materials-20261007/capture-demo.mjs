import fs from 'node:fs/promises';
import { chromium } from 'playwright';
const out='/path/to/SmartFactory-2026/output/playwright/competition-materials';
const c=JSON.parse(await fs.readFile('/path/to/SmartFactory-2026/.local/competition-materials-20261007/demo/.local/development-credentials.json','utf8'));
const browser=await chromium.launch({headless:true,executablePath:'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'});
const page=await browser.newPage({viewport:{width:1440,height:900},deviceScaleFactor:2,locale:'zh-CN'});
const login=async url=>{
 await page.goto(url);
 if(await page.getByLabel('账号',{exact:true}).count()) {
  await page.getByLabel('账号',{exact:true}).fill('admin');
  await page.getByLabel('密码',{exact:true}).fill(c.password);
  await page.getByRole('button',{name:'登录',exact:true}).click();
 }
 await page.waitForTimeout(1500);
};
try{
 await login('http://127.0.0.1:8090/');
 for(const [route,name] of [['overview','cloud-live'],['data','data-live'],['history','history-live']]){
  await page.goto('http://127.0.0.1:8090/#'+route);
  await page.waitForTimeout(1200);
  await page.screenshot({path:out+'/'+name+'.png'});
  await fs.writeFile(out+'/'+name+'.txt',await page.locator('body').innerText());
 }
 await page.goto('http://127.0.0.1:8090/screen');
 await page.waitForTimeout(1400);
 await page.screenshot({path:out+'/screen-live.png'});
 await login('http://127.0.0.1:8091/edge');
 await page.setViewportSize({width:430,height:900});
 await page.screenshot({path:out+'/edge-mobile.png'});
 await page.setViewportSize({width:1440,height:900});
 await page.goto('http://127.0.0.1:8091/edge#protocol-configurations');
 await page.waitForTimeout(1400);
 await fs.writeFile(out+'/edge-protocols.txt',await page.locator('body').innerText());
 await page.screenshot({path:out+'/edge-protocols.png'});
 console.log('Captured live cloud, screen, edge and data pages.');
}finally{await browser.close();}
