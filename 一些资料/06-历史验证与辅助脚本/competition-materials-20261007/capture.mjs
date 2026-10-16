import fs from 'node:fs/promises';
import { chromium } from 'playwright';
const out='/path/to/SmartFactory-2026/output/playwright/competition-materials';
const credentials=JSON.parse(await fs.readFile('/private/tmp/sf-materials-ui-20261007/private-credentials.json','utf8'));
const browser=await chromium.launch({headless:true,executablePath:'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'});
const page=await browser.newPage({viewport:{width:1600,height:1000},deviceScaleFactor:2,locale:'zh-CN'});
try {
 await page.goto('http://127.0.0.1:19340/');
 await page.getByLabel('账号',{exact:true}).fill(credentials.login);
 await page.getByLabel('密码',{exact:true}).fill(credentials.password);
 await page.getByRole('button',{name:'登录',exact:true}).click();
 await page.getByRole('navigation',{name:'主导航'}).waitFor();
 for(const [route,name] of [['analysis','editor-focus'],['work-orders','work-detail-focus']]){
  await page.goto('http://127.0.0.1:19340/#'+route);
  await page.waitForTimeout(1200);
  if(route==='analysis') await page.getByText('温湿度与通风 · 一分钟平均温度',{exact:true}).click();
  if(route==='protocol-configurations') await page.getByRole('button',{name:'查看连接器参数',exact:true}).nth(1).click();
  if(route==='alarm-workbench') await page.getByRole('button',{name:'查看告警详情',exact:true}).first().click();
  if(route==='work-orders') await page.getByRole('button',{name:'工单详情',exact:true}).click();
  await page.waitForTimeout(600);
  const clip=route==='analysis'?{x:160,y:0,width:1440,height:795}:{x:600,y:0,width:1000,height:455};
  await page.screenshot({path:out+'/'+name+'.png',clip});
  await fs.writeFile(out+'/'+name+'.txt',await page.locator('body').innerText());
 }
 console.log('Captured detailed product views.');
} finally {await browser.close();}
