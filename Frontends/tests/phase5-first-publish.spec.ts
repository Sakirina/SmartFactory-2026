import { test, expect } from './phase5-support';
import { api, capture, choose, get, login, route, snapshot } from './phase5-support';
import { readFile } from 'node:fs/promises';
test('initial empty publication reproduces missing creation path before repair', async ({ page }) => {
  test.skip(process.env.SF_PHASE5_CASES !== 'initial-before');
  await login(page, 'node'); await route(page, 'releases'); const records = await api(page, '/releases'); expect(JSON.parse(records.text)).toEqual([]);
  await page.getByRole('button', { name: '组合固定发布', exact: true }).click();
  await expect(page.getByRole('button', { name: '创建首份发布清单', exact: true })).toHaveCount(0);
  await page.getByRole('combobox', { name: '参考已有固定清单', exact: true }).click();
  await expect(page.locator('.ant-select-item-option-content')).toHaveCount(0);
  await expect(page.getByRole('button', { name: '校验依赖与兼容', exact: true })).toBeDisabled();
  await expect(page.getByRole('button', { name: '登记固定发布', exact: true })).toBeDisabled();
  await capture(page, 'empty-directory-first-publication-blocked');
});
test('initial empty directory creates first fixed release from formal program, rule and configuration', async ({ page }) => {
  test.skip(process.env.SF_PHASE5_CASES !== 'initial-fixed');
  if (process.env.SF_PHASE5_FIRST_FIXTURE) {
    const fixture = JSON.parse(await readFile(process.env.SF_PHASE5_FIRST_FIXTURE, 'utf8'));
    const session = JSON.parse(await readFile(fixture.authority_session_file, 'utf8'));
    const { instrument } = await import('./phase5-support'); await instrument(page);
    await page.addInitScript(token => sessionStorage.setItem('smartfactory.session.' + location.host, token), session.token);
    await page.goto((process.env.SF_PHASE5_FIRST_URL || 'http://127.0.0.1:19616') + '/index.html#releases');
    await expect(page.getByRole('button', { name: '退出', exact: true })).toBeVisible();
  } else { await login(page, 'node'); await route(page, 'releases'); }
  const records = await api(page, '/releases'); expect(JSON.parse(records.text)).toEqual([]);
  await snapshot('first-release-empty-directory', { status: records.status, records: JSON.parse(records.text) });
  await page.getByRole('button', { name: '组合固定发布', exact: true }).click();
  await page.getByRole('button', { name: '创建首份发布清单', exact: true }).click();
  const id = process.env.SF_PHASE5_FIRST_FIXTURE ? 'frontend-first-isolated-' + Date.now() : 'frontend-first-release';
  await page.getByLabel('固定发布身份', { exact: true }).fill(id);
  await page.getByLabel('固定发布名称', { exact: true }).fill('无既有清单的首次发布');
  await choose(page, '已登记程序工件', 'phase5-v3-a01');
  await choose(page, '规则和配置组件', 'frontend-initial-rule');
  await choose(page, '规则和配置组件', 'parameter:heartbeat.interval_ms');
  await choose(page, '规则和配置组件', 'connector:edge-a/mqtt-a');
  await page.keyboard.press('Escape');
  await page.getByRole('button', { name: '校验依赖与兼容', exact: true }).click();
  await expect(page.getByText('依赖与兼容校验通过', { exact: true })).toBeVisible();
  await capture(page, 'first-release-fixed-validation');
  await page.getByRole('button', { name: '登记固定发布', exact: true }).click(); await expect(page.getByRole('dialog', { name: '组合程序、规则和配置' })).toBeHidden();
  const saved = JSON.parse((await api(page, '/releases/' + id)).text); expect(saved.manifest.components).toHaveLength(4); expect(saved.manifest.components.find((component: any) => component.kind === 'configuration' && component.configuration.kind === 'connector').configuration.id).toBe('edge-a/mqtt-a');
  await expect(page.getByRole('row').filter({ hasText: id }).first()).toContainText('无既有清单的首次发布'); await snapshot('first-release-fixed-record', saved);
  await capture(page, 'first-release-fixed-saved');
});
test('first empty-directory publication remains fixed after correcting the drawer assertion', async ({ page }) => {
  test.skip(process.env.SF_PHASE5_CASES !== 'initial-reconcile');
  await login(page, 'node'); await route(page, 'releases');
  const saved = await get(page, '/releases/frontend-first-release');
  expect(saved.manifest.components).toHaveLength(4);
  expect(saved.manifest.components.map((component: any) => component.kind).sort()).toEqual(['configuration', 'configuration', 'program', 'rule']);
  expect(saved.manifest.components.find((component: any) => component.configuration?.kind === 'connector').configuration.id).toBe('edge-a/mqtt-a');
  expect(saved.sha256).toBe('2db0b5ae06d68845d646f7c5dc6731a027ddd0b8d45c6efeb16b4d49df8524fd');
  await expect(page.getByRole('row').filter({ hasText: 'frontend-first-release' }).first()).toContainText('无既有清单的首次发布');
  await snapshot('first-release-fixed-record', saved); await capture(page, 'first-release-fixed-saved');
});
