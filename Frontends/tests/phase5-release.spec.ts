import { test, expect } from './phase5-support';
import { api, capture, choose, get, login, route, snapshot } from './phase5-support';
test('release interface reads fixed manifests, current process and workload reports', async ({ page }) => {
  await login(page); await route(page, 'releases?deployment=deploy-validation-a01-b');
  await expect(page.getByTestId('release-detail')).toContainText('deploy-validation-a01-b');
  await expect(page.getByTestId('report-fresh-release-b')).toContainText('实际报告有效');
  await page.locator('[data-testid="release-detail"] .ant-table-row-expand-icon').last().click();
  await expect(page.getByTestId('release-detail')).toContainText('phase5-v3-a01');
  await capture(page, 'release-detail-first');
  await route(page, 'workload-identities'); await expect(page.getByRole('heading', { name: '节点身份与配置报告' })).toBeVisible();
  await expect(page.getByText('release-a', { exact: true })).toBeVisible(); await capture(page, 'release-workloads-first');
});
test('release composer validates immutable program, rule and configuration selection', async ({ page }) => {
  await login(page); await route(page, 'releases'); await page.getByRole('button', { name: '组合固定发布', exact: true }).click();
  await choose(page, '参考已有固定清单', 'release-validation-a01-b');
  const id = 'frontend-fixed-' + Date.now(), name = '页面组合固定内容 ' + id;
  await page.getByLabel('固定发布身份', { exact: true }).fill(id);
  await page.getByLabel('固定发布名称', { exact: true }).fill(name);
  await page.getByRole('button', { name: '校验依赖与兼容', exact: true }).click();
  await expect(page.getByText('依赖与兼容校验通过', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: '登记固定发布', exact: true }).click();
  await expect(page.getByRole('dialog', { name: '组合程序、规则和配置' })).toBeHidden();
  await expect(page.getByRole('row').filter({ hasText: id }).first()).toContainText(name); await capture(page, 'composer-first');
  const saved = await get(page, '/releases/' + id); expect(saved.manifest.components.some((component: any) => component.kind === 'program')).toBe(true); expect(saved.manifest.components.some((component: any) => component.kind === 'rule')).toBe(true); expect(saved.manifest.components.some((component: any) => component.kind === 'configuration')).toBe(true); await snapshot('composer-fixed-record', saved);
  const records = await api(page, '/releases'); expect(records.status).toBe(200);
});
