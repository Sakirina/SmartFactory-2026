import { test, expect } from '@playwright/test';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { createHash } from 'node:crypto';

const credentials = JSON.parse(readFileSync(resolve('../.local/development-credentials.json'),'utf8'));

test('assistant creates and modifies all three draft kinds through the model simulator', async ({page,request}) => {
  test.setTimeout(120000);
  const login = await request.post('/api/sf/v1/login',{data:{login:'admin',password:credentials.password}});
  const token=(await login.json()).token;
  const headers={Authorization:'Bearer '+token};
  const before = await (await request.get('/api/sf/v1/definitions',{headers})).json();
  await page.goto('/');
  await page.getByLabel('账号',{exact:true}).fill('admin');
  await page.getByLabel('密码',{exact:true}).fill(credentials.password);
  await page.getByRole('button',{name:'登录',exact:true}).click();
  await page.getByRole('button',{name:'AI 助手',exact:true}).click();
  const send = async (message:string) => {
    await page.getByLabel('发送给 AI 助手的消息').fill(message);
    await page.getByRole('button',{name:'发送',exact:true}).click();
  };
  for (const kind of ['analysis','alarm','strategy']) {
    const prompt=`创建 ${kind} 草稿，验收编号 ${Date.now()}`;
    const id=`ai-${kind}-${createHash('sha256').update(prompt).digest('hex').slice(0,10)}`;
    await send(prompt);
    await expect(page.locator('.message-assistant').last()).toContainText('模拟草稿已保存');
    await expect(page.locator('.message-assistant').last()).toContainText(id);
    const name='页面验收修改 '+kind;
    await send(`修改草稿 ${id} 名称=${name}`);
    await expect(page.locator('.message-assistant').last()).toContainText('模拟草稿已修改');
    const drafts = await (await request.get('/api/sf/v1/drafts',{headers})).json();
    const draft=drafts.find((item:{id:string})=>item.id===id);
    expect(draft.version).toBe(2);
    expect(draft.definition.name).toBe(name);
    const validation=await request.post(`/api/sf/v1/drafts/${id}/validate`,{headers,data:{}});
    expect((await validation.json()).valid).toBe(true);
  }
  await send('直接发布正式策略');
  await expect(page.locator('.message-assistant').last()).toContainText('拒绝了请求');
  await send('模拟服务错误');
  await expect(page.getByText('本次对话未完成',{exact:true})).toBeVisible();
  await expect(page.getByText('model endpoint returned HTTP 503',{exact:true})).toBeVisible();
  await page.screenshot({path:resolve('../.local/evidence/assistant-error.png'),fullPage:true,animations:'disabled'});
  const after=await (await request.get('/api/sf/v1/definitions',{headers})).json();
  expect(after).toEqual(before);
});
