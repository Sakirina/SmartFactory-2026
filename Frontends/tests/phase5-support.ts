import { test as base, expect, type Page } from '@playwright/test';
import { readFile, mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { parseExactJSON, stringifyExactJSON } from '../src/precision';
export const directory = process.env.SF_PHASE5_DIRECTORY || '/private/tmp/smartfactory-release-interface-20261005';
export const evidence = process.env.SF_PHASE5_EVIDENCE || path.join(directory, 'browser-first');
export const releaseCloud = process.env.SF_PHASE5_RELEASE_CLOUD || 'http://127.0.0.1:19610';
export const releaseEdge = process.env.SF_PHASE5_RELEASE_EDGE || 'http://127.0.0.1:19611';
export const nodeCloud = process.env.SF_PHASE5_NODE_CLOUD || 'http://127.0.0.1:19612';
export const nodeEdge = process.env.SF_PHASE5_NODE_EDGE || 'http://127.0.0.1:19613';
const records = new Map<Page, { requests: unknown[]; request_starts: unknown[]; console: unknown[]; errors: string[]; failed_resources: unknown[] }>();
export { expect };
export const test = base.extend({
  page: async ({ page }, use, info) => {
    await use(page);
    if (info.status !== info.expectedStatus && records.has(page)) {
      const name = 'failure-' + info.title.replace(/[^a-zA-Z0-9-]/g, '-').slice(0, 100);
      await capture(page, name).catch(async () => { await writeFile(path.join(evidence, name + '.page.json'), JSON.stringify(records.get(page), null, 2)); });
    }
  },
});
const clean = (value: unknown): unknown => Array.isArray(value) ? value.map(clean) : value && typeof value === 'object' ? Object.fromEntries(Object.entries(value).map(([key, item]) => [key, /^(credential|password|token|authorization|api_key|private_key)$/i.test(key) || (key === 'value' && (value as any).secret === true) ? '[redacted]' : clean(item)])) : value;
export async function instrument(page: Page) {
  await mkdir(evidence, { recursive: true }); const record = { requests: [] as unknown[], request_starts: [] as unknown[], console: [] as unknown[], errors: [] as string[], failed_resources: [] as unknown[] }; records.set(page, record);
  page.on('request', request => record.request_starts.push({ url: request.url(), method: request.method(), at_ms: Date.now() }));
  page.on('pageerror', error => record.errors.push(error.message));
  page.on('console', message => { if (['warning', 'error'].includes(message.type())) record.console.push({ type: message.type(), text: message.text(), location: message.location() }); });
  page.on('requestfailed', request => record.failed_resources.push({ url: request.url(), method: request.method(), failure: request.failure()?.errorText, at_ms: Date.now() }));
  page.on('response', async response => { if (response.status() >= 400) record.failed_resources.push({ url: response.url(), status: response.status(), method: response.request().method(), at_ms: Date.now() }); if (!response.url().includes('/api/sf/') || /\/login|\/logout|\/me/.test(response.url())) return;
    const request = response.request(); let body: unknown; try { body = clean(parseExactJSON(request.postData() || 'null')); } catch { body = 'non-json'; }
    record.requests.push({ method: request.method(), url: response.url(), status: response.status(), at_ms: Date.now(), body_raw: stringifyExactJSON(body) });
  });
}
export async function login(page: Page, kind = 'release', surface = 'index.html', viewer = false) {
  await instrument(page);
  const isNode = kind === 'node' || kind === 'node-edge';
  const descriptor = JSON.parse(await readFile(process.env[isNode ? 'SF_PHASE5_NODE_FIXTURE' : 'SF_PHASE5_RELEASE_FIXTURE'] || (isNode ? '/private/tmp/smartfactory-phase5-node-frontend-live-20261005/frontend.json' : '/private/tmp/smartfactory-release-restore-recovery-20261005/frontend-fixture.json'), 'utf8'));
  const original = viewer && !descriptor.readonly_credentials_file ? JSON.parse(await readFile('/private/tmp/smartfactory-phase5-release-a01-20261005/frontend-fixture.json', 'utf8')) : descriptor;
  const privateData = await readFile(viewer ? original.readonly_credentials_file : descriptor.credentials_file || descriptor.password_file, 'utf8');
  const password = isNode ? JSON.parse(privateData).password : privateData.trim();
  await page.goto((kind === 'release-edge' ? releaseEdge : kind === 'node-edge' ? nodeEdge : kind === 'node' ? nodeCloud : releaseCloud) + '/' + surface);
  await page.getByLabel('账号', { exact: true }).fill(viewer ? original.readonly_login : descriptor.login || 'admin');
  await page.getByLabel('密码', { exact: true }).fill(password);
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await page.getByLabel('密码', { exact: true }).fill('', { timeout: 500 }).catch(() => {});
  await expect(page.getByRole('button', { name: '退出', exact: true })).toBeVisible();
}
export async function api(page: Page, resource: string, body?: unknown) {
  return page.evaluate(async ({ resource, body }) => { const response = await fetch('/api/sf/v1' + resource, { method: body === undefined ? 'GET' : 'POST', headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + sessionStorage.getItem('smartfactory.session.' + location.host) }, body: body === undefined ? undefined : body }); return { status: response.status, text: await response.text() }; }, { resource, body: body === undefined ? undefined : stringifyExactJSON(body) });
}
export async function capture(page: Page, name: string) {
  await page.screenshot({ path: path.join(evidence, name + '.png'), fullPage: true });
  const record = records.get(page)!; await writeFile(path.join(evidence, name + '.page.json'), JSON.stringify({ url: page.url(), viewport: page.viewportSize(), ...record }, null, 2)); expect(record.errors).toEqual([]);
}
export async function route(page: Page, hash: string) { await page.evaluate(hash => { location.hash = hash; }, hash); }
export async function choose(page: Page, label: string, option: string) {
  const input = page.getByRole('combobox', { name: label, exact: true }); await input.click();
  if (await input.getAttribute('readonly') === null) await input.fill(option, { timeout: 12000 });
  const listID = await input.getAttribute('aria-controls');
  const popup = listID ? page.locator('[id="' + listID + '"]').locator('xpath=ancestor::*[contains(@class,"ant-select-dropdown")][1]') : page.locator('.ant-select-dropdown:visible');
  const candidate = popup.locator('.ant-select-item-option-content:visible').filter({ hasText: option }).first();
  await expect(candidate.locator('..')).not.toHaveClass(/ant-select-item-option-disabled/);
  await candidate.click({ timeout: 12000 });
}
export async function snapshot(name: string, value: unknown) {
  await mkdir(evidence, { recursive: true });
  await writeFile(path.join(evidence, name + '.state.json'), stringifyExactJSON(clean(value), 2));
}
export async function get<T = any>(page: Page, resource: string): Promise<T> {
  const result = await api(page, resource); expect(result.status, resource).toBe(200); return parseExactJSON(result.text) as T;
}
