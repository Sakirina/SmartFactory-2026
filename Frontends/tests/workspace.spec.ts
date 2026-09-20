import { test, expect } from '@playwright/test';
import { readFileSync, mkdirSync } from 'node:fs';
import { resolve } from 'node:path';
import { parseAPIJSON } from '../src/precision';
const credentials = JSON.parse(readFileSync(resolve('../.local/development-credentials.json'), 'utf8'));
mkdirSync(resolve('../.local/evidence'), { recursive: true });
for (const profile of [{ name: 'cloud', url: '/', width: 1440, height: 900, title: '工厂运行概览' }, { name: 'edge', url: 'http://127.0.0.1:8091/edge', width: 390, height: 844, title: '现场运行概览' }, { name: 'screen', url: '/screen', width: 1920, height: 1080, title: '示例工厂 · 生产安全运行' }]) {
  test(profile.name + ' layout and authenticated API', async ({ page }) => {
    await page.setViewportSize({ width: profile.width, height: profile.height });
    const errors: string[] = []; page.on('pageerror', e => errors.push(e.message));
    await page.goto(profile.url);
    await page.getByLabel('账号', { exact: true }).fill('admin');
    await page.getByLabel('密码', { exact: true }).fill(credentials.password);
    await page.getByRole('button', { name: '登录', exact: true }).click();
    await expect(page.getByRole('heading', { name: profile.title, exact: true })).toBeVisible();
    await expect(page.getByText('本次更新未完成', { exact: true })).toHaveCount(0);
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    expect(overflow).toBeLessThanOrEqual(1);
    await page.screenshot({ path: resolve(`../.local/evidence/${profile.name}-${profile.width}.png`), fullPage: true, animations: 'disabled' });
    expect(errors).toEqual([]);
    if (profile.name === 'cloud') {
      await page.getByRole('button', { name: '数据分析', exact: true }).click();
      await expect(page.locator('.definition-card').getByText('车间温度一分钟平均值', { exact: true })).toBeVisible();
      await page.getByRole('button', { name: '编辑与评审', exact: true }).first().click();
      await expect(page.getByRole('tab', { name: '可视化编排' })).toBeVisible();
      await expect(page.locator('.react-flow__node')).toHaveCount(3);
      await page.screenshot({ path: resolve('../.local/evidence/editor-1440.png'), fullPage: true, animations: 'disabled' });
      await page.getByRole('button', { name: '关闭', exact: true }).click();
      await page.getByRole('button', { name: '业务审计', exact: true }).click();
      await page.getByRole('button', { name: '校验审计完整性' }).click();
      await expect(page.getByText('审计校验通过', { exact: true })).toBeVisible();
    }
  });
}

test('64-bit telemetry stays exact in frontend decoding', () => {
  expect(parseAPIJSON('{"value":9007199254740993,"negative":-9223372036854775808,"unsigned":18446744073709551615,"text":"9007199254740993","decimal":1.25,"sequence":12}')).toEqual({ value: '9007199254740993', negative: '-9223372036854775808', unsigned: '18446744073709551615', text: '9007199254740993', decimal: 1.25, sequence: 12 });
});

test('draft simulation, version review and operational pages', async ({ page }) => {
  await page.goto('/');
  await page.getByLabel('账号', { exact: true }).fill('admin');
  await page.getByLabel('密码', { exact: true }).fill(credentials.password);
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await page.getByRole('button', { name: '数据分析', exact: true }).click();
  await page.getByRole('button', { name: '版本记录', exact: true }).first().click();
  await expect(page.getByRole('button', { name: '以此版本创建回滚草稿' })).toBeEnabled();
  await page.getByRole('button', { name: '关闭', exact: true }).last().click();
  await expect(page.getByRole('dialog', { name: '版本记录与回滚评审', exact: true })).toBeHidden();
  await page.getByRole('button', { name: '编辑与评审', exact: true }).first().click();
  await page.getByRole('button', { name: '模拟运行', exact: true }).click();
  await page.getByRole('button', { name: '运行模拟', exact: true }).click();
  await expect(page.getByRole('heading', { name: '节点输出', exact: true })).toBeVisible();
  await expect(page.getByText('模拟未完成', { exact: true })).toHaveCount(0);
  await page.getByRole('button', { name: '关闭', exact: true }).last().click();
  await expect(page.getByRole('dialog', { name: '草稿模拟运行', exact: true })).toBeHidden();
  await page.getByRole('dialog', { name: '车间温度一分钟平均值', exact: true }).getByRole('button', { name: '关闭', exact: true }).click();
  for (const title of ['历史补算', '通知记录', '同步插件', '配置中心']) {
    await page.getByRole('button', { name: title, exact: true }).click();
    await expect(page.getByRole('heading', { name: title, exact: true })).toBeVisible();
    await expect(page.getByText('本次更新未完成', { exact: true })).toHaveCount(0);
  }
  await page.getByRole('button', { name: '登记参数或凭据' }).click();
  await expect(page.getByLabel('参数标识', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: '关闭', exact: true }).click();
});

test('edge asset proposal requires and receives cloud review', async ({ request }) => {
  const login = async (base: string) => {
    const response = await request.post(base + '/api/sf/v1/login', { data: { login: 'admin', password: credentials.password } });
    expect(response.ok()).toBeTruthy(); return (await response.json()).token as string;
  };
  const edgeToken = await login('http://127.0.0.1:8091');
  const cloudToken = await login('http://127.0.0.1:8090');
  const id = 'qa-asset-' + Date.now();
  const proposalResponse = await request.post('http://127.0.0.1:8091/api/sf/v1/entities', { headers: { Authorization: 'Bearer ' + edgeToken }, data: { entity: { id, name: '流程验收资产', kind: 'asset', parent_id: 'workshop', status: 'active', version: 0 }, expected_version: 0 } });
  expect(proposalResponse.status()).toBe(202);
  const proposal = await proposalResponse.json();
  await expect.poll(async () => { const response = await request.get('/api/sf/v1/asset-proposals', { headers: { Authorization: 'Bearer ' + cloudToken } }); return (await response.json()).some((item: { id: string }) => item.id === proposal.id); }).toBe(true);
  const decision = await request.post(`/api/sf/v1/asset-proposals/${encodeURIComponent(proposal.id)}/decide`, { headers: { Authorization: 'Bearer ' + cloudToken }, data: { approve: true, expected_version: 1, reason: '自动化验证现场申请和云端评审' } });
  expect(decision.ok()).toBeTruthy();
  await expect.poll(async () => { const response = await request.get('http://127.0.0.1:8091/api/sf/v1/entities', { headers: { Authorization: 'Bearer ' + edgeToken } }); return (await response.json()).some((item: { id: string; status: string }) => item.id === id && item.status === 'active'); }).toBe(true);
});

test('dashboard configuration persists and metric details use live data', async ({ page }) => {
  await page.setViewportSize({ width: 1920, height: 1080 });
  await page.goto('/screen');
  await page.getByLabel('账号', { exact: true }).fill('admin');
  await page.getByLabel('密码', { exact: true }).fill(credentials.password);
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await page.getByRole('button', { name: '配置大屏', exact: true }).click();
  await page.getByRole('button', { name: '另存为新的大屏', exact: true }).click();
  const title = '流程验收大屏 ' + Date.now();
  await page.getByLabel('大屏标题', { exact: true }).fill(title);
  await page.getByRole('button', { name: '保存大屏', exact: true }).click();
  await expect(page.getByRole('dialog', { name: '配置运行大屏', exact: true })).toBeHidden();
  await expect(page.getByRole('heading', { name: title, exact: true })).toBeVisible();
  await page.reload();
  await expect(page.getByRole('heading', { name: title, exact: true })).toBeVisible();
  await page.locator('.indicator-button').first().click();
  await expect(page.getByRole('dialog').getByText('climate-1 / temperature · 最近 1 小时', { exact: true })).toBeVisible();
  await expect(page.getByRole('dialog').locator('.ant-table-tbody tr').first()).toBeVisible();
  await page.screenshot({ path: resolve('../.local/evidence/screen-metric-detail.png'), fullPage: true, animations: 'disabled' });
  await page.getByRole('button', { name: '关闭', exact: true }).click();
  await page.getByRole('button', { name: '退出', exact: true }).click();
  await page.getByLabel('账号', { exact: true }).fill('viewer');
  await page.getByLabel('密码', { exact: true }).fill(credentials.password);
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await expect(page.getByRole('heading', { name: title, exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: '配置大屏', exact: true })).toHaveCount(0);
});
