import { test, expect, api, capture, get, login, route, snapshot } from './phase5-support';
import { parseExactJSON } from '../src/precision';
async function editRow(page: any, id: string) {
  await page.getByLabel('筛选节点身份', { exact: true }).fill(id);
  await page.getByRole('row').filter({ hasText: id }).first().getByRole('button', { name: '编辑范围或停用', exact: true }).click();
}
test('workload false and empty scopes persist and delayed conflict refresh preserves new identity input', async ({ page }) => {
  test.skip(!['identity-basis', 'identity-basis-before'].includes(process.env.SF_PHASE5_CASES || ''));
  const before = process.env.SF_PHASE5_CASES === 'identity-basis-before';
  await login(page); await route(page, 'workload-identities');
  const id = 'frontend-empty-scope-' + Date.now();
  await page.getByRole('button', { name: '登记工作负载身份', exact: true }).click();
  await page.getByLabel('工作负载标识', { exact: true }).fill(id); await page.getByLabel('归属节点', { exact: true }).fill('edge-a');
  await page.getByLabel('使用用途', { exact: true }).fill('前端显式空范围验证');
  await page.getByRole('switch', { name: '启用身份', exact: true }).uncheck();
  const scope = page.locator('.ant-form-item').filter({ has: page.getByRole('combobox', { name: '允许的能力', exact: true }) });
  for (let index = 0; index < 3; index++) await scope.locator('.ant-select-selection-item-remove').first().click();
  await page.getByRole('button', { name: '保存身份范围', exact: true }).click(); await expect(page.getByRole('dialog', { name: '编辑工作负载身份' })).toBeHidden();
  const original = (await get<any[]>(page, '/workload-identities')).find(item => item.id === id);
  expect(original.enabled).toBe(false); expect(original.capabilities).toEqual([]); expect(original.parameter_ids).toEqual([]); expect(original.connector_ids).toEqual([]);
  await snapshot('identity-explicit-empty', original); await capture(page, 'identity-explicit-empty');
  await editRow(page, id); await page.getByLabel('使用用途', { exact: true }).fill('冲突前的未提交用途');
  const changed = await api(page, '/workload-identities', { identity: { ...original, purpose: '另一会话的授权修改' }, expected_version: original.version }); expect(changed.status).toBe(200);
  const current = parseExactJSON(changed.text) as any;
  const response = page.waitForResponse(value => value.request().method() === 'POST' && value.url().endsWith('/workload-identities'));
  await page.getByRole('button', { name: '保存身份范围', exact: true }).click(); expect((await response).status()).toBe(409);
  await expect(page.getByText('身份范围未保存', { exact: true })).toBeVisible();
  let release!: () => void, started!: () => void, finished!: () => void;
  const held = new Promise<void>(resolve => { release = resolve; }), begun = new Promise<void>(resolve => { started = resolve; }), done = new Promise<void>(resolve => { finished = resolve; });
  let armed = true;
  await page.route('**/api/sf/v1/workload-identities', async request => {
    if (armed && request.request().method() === 'GET') { armed = false; const real = await request.fetch(); started(); await held; await request.fulfill({ response: real }); finished(); } else await request.continue();
  });
  await page.getByRole('button', { name: '保留编辑并更新已阅版本', exact: true }).click(); await begun;
  const typed = '响应等待期间新输入的用途'; await page.getByLabel('使用用途', { exact: true }).fill(typed);
  release(); await done; await page.unroute('**/api/sf/v1/workload-identities');
  await expect(page.getByRole('dialog', { name: '编辑工作负载身份' })).toContainText('已阅业务版本 ' + current.version);
  const actual = await page.getByLabel('使用用途', { exact: true }).inputValue();
  await snapshot('identity-delayed-refresh', { original, changed: current, typed, actual, expected_after_refresh: current.version }); await capture(page, 'identity-delayed-refresh');
  if (before) { expect(actual).toBe('冲突前的未提交用途'); return; }
  expect(actual).toBe(typed);
  const saved = page.waitForResponse(value => value.request().method() === 'POST' && value.url().endsWith('/workload-identities'));
  await page.getByRole('button', { name: '保存身份范围', exact: true }).click(); expect((await saved).status()).toBe(200);
  const verified = (await get<any[]>(page, '/workload-identities')).find(item => item.id === id);
  expect(verified.purpose).toBe(typed); expect(verified.version).toBe(current.version + 1);
  await snapshot('identity-delayed-refresh-saved', verified); await capture(page, 'identity-delayed-refresh-saved');
});
