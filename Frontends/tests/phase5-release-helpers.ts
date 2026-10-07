import type { Page } from '@playwright/test';
import { expect, api, capture, choose, get, route } from './phase5-support';
export async function waitDeployment(page: Page, id: string, condition: (value: any) => boolean, timeout = 50000) {
  let value: any;
  await expect.poll(async () => { value = await get(page, '/release-deployments/' + id); return condition(value); }, { timeout, intervals: [300, 500, 1000] }).toBe(true);
  return value;
}
export async function createBatch(page: Page, id: string, release: string, batches: string[][], reason: string) {
  await route(page, 'releases'); await page.getByRole('button', { name: '新建节点批次', exact: true }).click();
  await page.getByLabel('发布批次身份', { exact: true }).fill(id);
  await choose(page, '待发布固定清单', release);
  for (let index = 0; index < batches.length; index++) {
    if (index) await page.getByRole('button', { name: '增加后续批次', exact: true }).click();
    for (const identity of batches[index]) { await choose(page, '第 ' + (index + 1) + ' 批节点身份', identity); await page.keyboard.press('Escape'); }
  }
  await page.getByLabel('分批发布原因', { exact: true }).fill(reason);
  await capture(page, id + '-selection');
  await page.getByRole('button', { name: '开始分批发布', exact: true }).click();
  await expect(page.getByRole('dialog', { name: '新建节点发布批次' })).toBeHidden();
  await expect(page.getByTestId('release-detail')).toContainText(id);
}
