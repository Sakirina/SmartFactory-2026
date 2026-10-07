import type { Page } from '@playwright/test';
import { test, expect } from './phase5-support';
import { unlink, writeFile } from 'node:fs/promises';
import { api, capture, get, login, route, snapshot } from './phase5-support';
import { nodeProcessRoot, restartNodeConsumer, signalFixture } from './phase5-fixture-controls';
const runtimeURL = { a: 'http://127.0.0.1:60904/runtime', b: 'http://127.0.0.1:60905/runtime' };
async function policy(node: 'a' | 'b') { const result = await fetch(runtimeURL[node], { signal: AbortSignal.timeout(3000) }); expect(result.status).toBe(200); return result.json(); }
async function editParameter(page: Page, id: string, value: unknown) {
  await route(page, 'configuration');
  await expect(page.getByRole('heading', { name: '配置中心', exact: true })).toBeVisible();
  const row = page.getByRole('row').filter({ hasText: id });
  for (let index = 0; index < 4 && !await row.count(); index++) {
    const next = page.locator('.ant-pagination-next:not(.ant-pagination-disabled)');
    if (await next.count()) await next.first().click();
    else await page.locator('.ant-pagination-item-1').first().click();
  }
  await row.getByRole('button', { name: '编辑', exact: true }).click();
  await page.getByLabel('用途说明', { exact: true }).fill('前端专项配置核对：' + id);
  await page.getByLabel('参数 JSON 值', { exact: true }).fill(JSON.stringify(value));
}
async function saveParameter(page: Page) { await page.getByRole('button', { name: /保存新版本$/ }).click(); await expect(page.getByRole('dialog', { name: '更新程序参数', exact: true })).toBeHidden(); await expect(page.getByText('配置新版本已保存', { exact: true }).last()).toBeVisible(); }
async function report(page: Page, id: string, version: any, state = 'running') {
  let value: any;
  await expect.poll(async () => { const reports = await get<any[]>(page, '/configuration-reports'); value = reports.find(item => item.id === id); return value?.desired.version === version && value?.state === state; }, { timeout: 20000, intervals: [250, 500] }).toBe(true);
  return value;
}
test('node configuration applies dynamic values, retains conflicting edits and rolls back failed consumers', async ({ page }) => {
  test.skip(process.env.SF_PHASE5_CASES !== 'node'); test.setTimeout(120000);
  await login(page, 'node'); const before = await policy('a');
  await editParameter(page, 'control.start_ttl_ms', 12345); await saveParameter(page);
  const saved = (await get<any[]>(page, '/config')).find(item => item.id === 'control.start_ttl_ms');
  const applied = await report(page, saved.id, saved.version); expect(applied.effective_value).toBe(12345);
  const after = await policy('a'); expect(after.policy.StartTTLMS).toBe(12345);
  await snapshot('dynamic-parameter-real-consumer', { before_policy: before.policy, after_policy: after.policy, report: applied, parameter: saved });
  await editParameter(page, saved.id, 54321);
  await expect(page.getByRole('dialog', { name: '更新程序参数', exact: true })).toBeVisible(); await capture(page, 'parameter-before-external-change');
  const external = await api(page, '/config', { parameter: { ...saved, value: 24680 }, expected_version: saved.version }); expect(external.status).toBe(200);
  await capture(page, 'parameter-after-external-change');
  await snapshot('parameter-save-button-diagnostic', await page.locator('.ant-drawer-header button').evaluateAll(elements => elements.map(element => ({ text: element.textContent, class: element.className, disabled: (element as HTMLButtonElement).disabled, rect: element.getBoundingClientRect().toJSON(), html: element.innerHTML }))));
  await page.getByRole('button', { name: /保存新版本$/ }).click(); await expect(page.getByText('配置新版本未保存', { exact: true })).toBeVisible();
  await expect(page.getByLabel('参数 JSON 值', { exact: true })).toHaveValue('54321'); await capture(page, 'parameter-conflict-retains-input');
  await page.getByRole('button', { name: '保留输入并更新已阅版本', exact: true }).click(); await saveParameter(page);
  const revised = (await get<any[]>(page, '/config')).find(item => item.id === saved.id); await report(page, saved.id, revised.version);
  expect((await policy('a')).policy.StartTTLMS).toBe(54321);
  const oldB = await policy('b'); const original = (await get<any[]>(page, '/config')).find(item => item.id === 'queue.capacity');
  await writeFile(nodeProcessRoot + '/private/reject-b', 'frontend consumer failure\n', { mode: 0o600 });
  try {
    await editParameter(page, original.id, 2500); await saveParameter(page);
    const failedParameter = (await get<any[]>(page, '/config')).find(item => item.id === original.id);
    const failed = await report(page, original.id, failedParameter.version, 'failed');
    const retained = await policy('b'); expect(retained.policy.QueueCapacity).toBe(oldB.policy.QueueCapacity); expect(failed.running.version).toBeLessThan(failed.desired.version);
    await route(page, 'workload-identities'); await expect(page.getByRole('row').filter({ hasText: 'parameter / queue.capacity' })).toContainText('处理失败');
    await snapshot('dynamic-failure-retains-prior-consumer', { old_policy: oldB.policy, actual_policy: retained.policy, report: failed }); await capture(page, 'dynamic-failure-report');
  } finally { await unlink(nodeProcessRoot + '/private/reject-b').catch(() => {}); }
  await editParameter(page, original.id, 2500); await saveParameter(page);
  const restored = (await get<any[]>(page, '/config')).find(item => item.id === original.id); const recovered = await report(page, original.id, restored.version);
  expect((await policy('b')).policy.QueueCapacity).toBe(2500); await snapshot('dynamic-failure-recovered', recovered); await capture(page, 'dynamic-configuration-recovered');
});
test('static configuration waits for a real new process and retains verified cache during config outage', async ({ page }) => {
  test.skip(process.env.SF_PHASE5_CASES !== 'node'); test.setTimeout(120000);
  await login(page, 'node'); const originalRuntime = await policy('a');
  const oldIdentity = (await get<any[]>(page, '/workload-identities')).find(item => item.id === 'workload-a');
  await editParameter(page, 'heartbeat.interval_ms', 7000); await saveParameter(page);
  const parameter = (await get<any[]>(page, '/config')).find(item => item.id === 'heartbeat.interval_ms'); const waiting = await report(page, parameter.id, parameter.version, 'restart_required');
  expect(waiting.prepared.version).toBe(parameter.version); expect(waiting.running.version).toBeLessThan(parameter.version); expect((await policy('a')).policy.HeartbeatMS).toBe(originalRuntime.policy.HeartbeatMS);
  await route(page, 'workload-identities'); await expect(page.getByRole('row').filter({ hasText: 'parameter / heartbeat.interval_ms' })).toContainText('需要重启');
  await snapshot('static-awaiting-new-process', waiting); await capture(page, 'static-awaiting-new-process');
  const restarted = await restartNodeConsumer('a'); const running = await report(page, parameter.id, parameter.version);
  const actual = await policy('a'); expect(actual.policy.HeartbeatMS).toBe(7000);
  const identity = (await get<any[]>(page, '/workload-identities')).find(item => item.id === 'workload-a'); expect(identity.instance_epoch).toBeGreaterThan(oldIdentity.instance_epoch); expect(identity.instance_id).not.toBe(oldIdentity.instance_id);
  await snapshot('static-running-after-real-restart', { restarted, old_identity: oldIdentity, identity, actual_policy: actual.policy, report: running }); await capture(page, 'static-running-after-real-restart');
  const processes = JSON.parse(await (await import('node:fs/promises')).readFile(nodeProcessRoot + '/private/running-processes.json', 'utf8'));
  const configurationPID = Number(processes.find((item: any) => item.name === 'config').pid);
  const stopped = signalFixture(configurationPID, 'sf-config', 'SIGSTOP');
  try {
    await page.getByRole('button', { name: '更新身份与报告', exact: true }).click();
    await expect(page.getByText('本次更新未完成', { exact: true })).toBeVisible({ timeout: 15000 });
    const cachedRestart = await restartNodeConsumer('a');
    let cached: any;
    await expect.poll(async () => { try { cached = await policy('a'); return cached.policy.HeartbeatMS; } catch { return 0; } }, { timeout: 12000, intervals: [250, 500] }).toBe(7000);
    await snapshot('config-outage-cached-real-process', { stopped, cached_restart: cachedRestart, cached_policy: cached.policy }); await capture(page, 'config-outage-visible');
  } finally { signalFixture(configurationPID, 'sf-config', 'SIGCONT'); }
  const recovered = await report(page, parameter.id, parameter.version);
  await page.getByRole('button', { name: '更新身份与报告', exact: true }).click(); await expect(page.getByText('本次更新未完成', { exact: true })).toBeHidden();
  await snapshot('config-outage-reauthenticated-report', recovered); await capture(page, 'config-outage-recovered');
});
