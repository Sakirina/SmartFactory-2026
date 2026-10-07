import { readFile, writeFile } from 'node:fs/promises';
import { test, expect, login, route, get, capture, snapshot, directory, evidence, choose } from './phase5-support';
import type { Page } from '@playwright/test';
import { parseExactJSON } from '../src/precision';

async function download(page: Page, button: string, name: string) {
  const pending = page.waitForEvent('download'); await page.getByRole('button', { name: button, exact: true }).click();
  const file = await (await pending).path(); expect(file).toBeTruthy(); const raw = await readFile(file!, 'utf8');
  await writeFile(evidence + '/' + name + '.export.json', raw); return raw;
}
async function hover(page: Page, scoped = page.getByRole('img', { name: /测点历史趋势/ }).first()) {
  await scoped.scrollIntoViewIfNeeded(); await page.mouse.move(0, 0); await page.waitForTimeout(250);
  const box = (await scoped.boundingBox())!;
  await page.mouse.move(box.x + box.width / 2 + 20, box.y + box.height / 2);
  await expect(page.locator('.sf-chart-tooltip:visible').first()).toContainText('9007199254740993');
  const text = await page.locator('.sf-chart-tooltip:visible').first().innerText(); expect(text).toContain('precision-source'); expect(text).toContain('count'); return text;
}
async function manifest(kind: string) { return parseExactJSON(await readFile(directory + '/precision-resumed/' + kind + '.state.json', 'utf8')) as any; }

test('cloud and owning edge tables tooltips and complete exports retain exact integers floats aggregates and strings', async ({ browser }) => {
  test.skip(process.env.SF_PHASE5_CASES !== 'precision'); test.setTimeout(90000);
  for (const kind of ['cloud', 'edge']) {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } }), page = await context.newPage(), input = await manifest(kind);
    await login(page, kind === 'edge' ? 'release-edge' : 'release', kind === 'edge' ? 'edge.html' : 'index.html');
    await route(page, 'data?device=' + input.device); await page.getByLabel('测点名称', { exact: true }).fill('precise_integer');
    await expect(page.getByText('9007199254740993', { exact: true })).toBeVisible(); const tooltip = await hover(page); await capture(page, kind + '-precise-tooltip');
    await page.getByLabel('测点名称', { exact: true }).fill(input.points.map((point: any) => point.key).join(','));
    const table = page.locator('.ant-card').filter({ has: page.getByText('精确观测记录', { exact: true }) });
    for (const value of ['18446744073709551615', '-9007199254740993', '12.345678901234567', 'true']) await expect(table).toContainText(value);
    const raw = await download(page, '导出完整精确数据', kind + '-precise');
    expect(raw).toMatch(/"value":\s*9007199254740993/); expect(raw).toMatch(/"value":\s*"9007199254740993"/); expect(raw).toMatch(/"average":\s*9007199254740993/);
    for (const value of ['18446744073709551615', '-9007199254740993', '12.345678901234567', 'true']) expect(raw).toContain(value);
    await snapshot(kind + '-precision', { tooltip, export_bytes: Buffer.byteLength(raw), input, exported_rows: JSON.parse(raw).items.length }); await capture(page, kind + '-precise-table-export'); await context.close();
  }
});

test('screen stores a precise metric and displays the complete value in its card detail tooltip and export', async ({ page }) => {
  test.skip(process.env.SF_PHASE5_CASES !== 'precision'); test.setTimeout(60000);
  const input = await manifest('cloud'); await login(page, 'release', 'screen.html');
  const entities = await get<any[]>(page, '/entities'); const name = entities.find(value => value.id === input.device).name;
  await page.getByRole('button', { name: '配置大屏', exact: true }).click(); const modal = page.getByRole('dialog', { name: '配置运行大屏' });
  while (await modal.getByRole('button', { name: '删除', exact: true }).count()) await modal.getByRole('button', { name: '删除', exact: true }).first().click();
  await modal.getByRole('button', { name: '添加指标', exact: true }).click();
  const inputDevice = modal.getByLabel('指标 1 的设备'); await inputDevice.click(); await inputDevice.press('Home');
  for (let step = 0; step < 50; step++) { const active = page.locator('.ant-select-dropdown:visible .ant-select-item-option-active .ant-select-item-option-content'); if (await active.count() && await active.innerText() === name) { await inputDevice.press('Enter'); break; } await inputDevice.press('ArrowDown'); }
  await modal.getByLabel('指标 1 的测点').fill('precise_integer'); await modal.getByLabel('指标 1 的标题').fill('最终发布精确累计指标');
  await modal.getByRole('button', { name: '保存大屏', exact: true }).click(); await expect(modal).toBeHidden();
  const metric = page.getByRole('button', { name: /最终发布精确累计指标/ }); await expect(metric).toContainText('9007199254740993'); await metric.click();
  const drawer = page.getByRole('dialog', { name: '最终发布精确累计指标' }); await expect(drawer.getByText('9007199254740993', { exact: true })).toBeVisible();
  const tooltip = await hover(page, drawer.getByRole('img', { name: /测点历史趋势/ })); await capture(page, 'screen-precise-detail-tooltip');
  await drawer.getByRole('button', { name: '关闭', exact: true }).click(); const raw = await download(page, '导出精确大屏数据', 'screen-precise');
  expect(raw).toMatch(/"value":\s*9007199254740993/); expect(raw).toContain('18446744073709551615'); await snapshot('screen-precision', { tooltip, dashboards: await get(page, '/dashboards'), export_bytes: Buffer.byteLength(raw) }); await capture(page, 'screen-precise-export');
});

test('all three entries render real charts at desktop tablet and phone sizes without document horizontal scrolling', async ({ browser }) => {
  test.skip(process.env.SF_PHASE5_CASES !== 'surfaces'); test.setTimeout(150000); const results = [];
  for (const [kind, surface] of [['release', 'index.html'], ['release-edge', 'edge.html'], ['release', 'screen.html']]) for (const [width, height] of [[1440, 1000], [1024, 768], [390, 844]]) {
    const context = await browser.newContext({ viewport: { width, height } }), page = await context.newPage(); await login(page, kind, surface);
    if (surface !== 'screen.html') { await route(page, 'data?device=' + (await manifest(kind === 'release-edge' ? 'edge' : 'cloud')).device); await page.getByLabel('测点名称', { exact: true }).fill('precise_integer'); await expect(page.getByText('9007199254740993', { exact: true })).toBeVisible(); }
    else await expect(page.getByRole('button', { name: /最终发布精确累计指标/ })).toContainText('9007199254740993');
    await expect(page.getByRole('img', { name: /测点历史趋势/ }).first()).toBeVisible(); await expect(page.locator('canvas').first()).toBeVisible();
    const layout = await page.getByRole('img', { name: /测点历史趋势/ }).first().evaluate(image => { const box = image.getBoundingClientRect(), canvas = image.querySelector('canvas')!; window.scrollTo(100, 0); return { document: document.documentElement.scrollWidth, body: document.body.scrollWidth, viewport: innerWidth, horizontal_scroll: scrollX, chart: { x: box.x, width: box.width, height: box.height, canvas_width: canvas.width, canvas_height: canvas.height }, note: document.querySelector('.chart-precision-note')?.textContent }; });
    expect(layout.document).toBeLessThanOrEqual(width); expect(layout.horizontal_scroll).toBe(0); expect(layout.chart.width).toBeGreaterThan(200); expect(layout.chart.canvas_width).toBeGreaterThan(0); expect(layout.note).toContain('精确原值');
    const name = kind + '-' + surface + '-' + width + 'x' + height; results.push({ kind, surface, width, height, layout }); await snapshot(name, layout); await capture(page, name); await context.close();
  }
  await snapshot('all-nine-surfaces', results);
});
