import type { Page } from '@playwright/test';
import { test, expect } from './phase5-support';
import { api, capture, choose, get, login, route, snapshot } from './phase5-support';
import { releaseAgentPID, releaseProcessRoot, signalFixture } from './phase5-fixture-controls';
import { parseExactJSON } from '../src/precision';
import { createBatch, waitDeployment } from './phase5-release-helpers';
import { readFile } from 'node:fs/promises';
import { directory } from './phase5-support';
test('actual node batches wait offline, preserve heartbeat basis, reject changed basis and recover', async ({ page }) => {
  test.skip(process.env.SF_PHASE5_CASES !== 'pipeline'); test.setTimeout(180000);
  await login(page); const id = 'frontend-offline-' + Date.now();
  await expect.poll(async () => (await get<any[]>(page, '/release-deployments')).filter(value => !['completed', 'cancelled'].includes(value.state) && value.targets?.some((target: any) => ['release-a', 'release-b'].includes(target.identity_id))).map(value => value.id), { timeout: 25000, intervals: [500, 1000] }).toEqual([]);
  const agent = await releaseAgentPID('b');
  const stopped = signalFixture(agent, releaseProcessRoot + '/private/agent-b', 'SIGSTOP');
  try {
    const normal = JSON.parse(await readFile(directory + '/normal-final-release.state.json', 'utf8'));
    await createBatch(page, id, normal.id, [['release-a'], ['release-b']], '先核对一号节点，再等待二号节点恢复联系');
    const waiting = await waitDeployment(page, id, d => d.current_batch === 1 && d.targets[0].state === 'running' && d.targets[0].report_fresh && d.targets[1].state !== 'running');
    expect(waiting.state).not.toBe('completed'); expect(Number(waiting.batches[0].completed_ms)).toBeGreaterThan(0); expect(Number(waiting.batches[1].completed_ms)).toBe(0);
    await snapshot(id + '-offline', { stopped, deployment: waiting }); await capture(page, id + '-offline');
    let releaseResponse: () => void = () => {};
    const delay = new Promise<void>(resolve => { releaseResponse = resolve; });
    let pending: () => void = () => {};
    const started = new Promise<void>(resolve => { pending = resolve; });
    let finished: () => void = () => {};
    const fulfilled = new Promise<void>(resolve => { finished = resolve; });
    let armed = true;
    await page.route('**/api/sf/v1/release-deployments/' + id, async request => {
      if (armed && request.request().method() === 'GET') { armed = false; const response = await request.fetch(); pending(); await delay; await request.fulfill({ response }); finished(); }
      else await request.continue();
    });
    await page.getByRole('button', { name: '核对状态并更新操作依据', exact: true }).click(); await started;
    await page.getByLabel('发布处理原因', { exact: true }).fill('保留输入并等待二号节点恢复');
    await choose(page, '退回的固定发布', 'release-v1');
    releaseResponse(); await fulfilled; await page.unroute('**/api/sf/v1/release-deployments/' + id);
    await expect(page.getByLabel('发布处理原因', { exact: true })).toHaveValue('保留输入并等待二号节点恢复');
    const before = await get(page, '/release-deployments/' + id);
    const reported = await waitDeployment(page, id, d => d.targets[0].last_report_ms > before.targets[0].last_report_ms + 1500);
    expect(reported.version).toBe(before.version); await snapshot(id + '-heartbeat-basis', { before, after: reported });
    await page.getByRole('button', { name: '暂停发布', exact: true }).click();
    const paused = await waitDeployment(page, id, d => d.state === 'paused');
    expect(paused.version).toBeGreaterThan(before.version); await capture(page, id + '-paused');
    const external = await api(page, '/release-deployments/' + id + '/actions', { request_id: id + '-external-resume', expected_version: paused.version, action: 'resume', reason: '另一已授权会话改变业务状态' });
    expect(external.status).toBe(200); const resumed = parseExactJSON(external.text) as any;
    await expect(page.getByRole('button', { name: '暂停发布', exact: true })).toBeEnabled();
    await page.getByRole('button', { name: '暂停发布', exact: true }).click();
    await expect(page.getByText('发布处理未完成', { exact: true })).toBeVisible();
    await expect(page.getByLabel('发布处理原因', { exact: true })).toHaveValue('保留输入并等待二号节点恢复');
    await expect(page.locator('.ant-form-item').filter({ has: page.getByRole('combobox', { name: '退回的固定发布', exact: true }) })).toContainText('release-v1');
    await snapshot(id + '-conflict', { external_status: external.status, externally_changed: resumed }); await capture(page, id + '-conflict');
    await page.getByRole('button', { name: '核对状态并更新操作依据', exact: true }).click();
    await expect(page.getByTestId('release-detail')).toContainText('已阅业务版本 ' + String(resumed.version));
    await page.getByRole('button', { name: '暂停发布', exact: true }).click();
    await waitDeployment(page, id, d => d.state === 'paused');
    await page.getByRole('button', { name: '继续发布', exact: true }).click();
    await waitDeployment(page, id, d => d.state === 'active');
    signalFixture(agent, releaseProcessRoot + '/private/agent-b', 'SIGCONT');
    const completed = await waitDeployment(page, id, d => d.state === 'completed' && d.targets.every((target: any) => target.state === 'running' && target.report_fresh));
    for (const target of completed.targets) { expect(target.runtime.program_sha256).toBe('b02566d928858835502d2d0cd6aac04ef561335e0498f7675fb1d60b0e71f229'); expect(target.running_sha256).toBe(completed.release_sha256); expect(target.runtime.components.some((component: any) => component.kind === 'configuration')).toBe(true); }
    await snapshot(id + '-completed', completed); await capture(page, id + '-completed');
    const pid = Number(completed.targets.find((target: any) => target.identity_id === 'release-b').runtime.pid);
    const frozen = signalFixture(pid, releaseProcessRoot + '/private/agent-b/artifacts/', 'SIGSTOP');
    try {
      const expired = await waitDeployment(page, id, d => { const target = d.targets.find((target: any) => target.identity_id === 'release-b'); return target.report_age_ms > 15000 && !target.report_fresh && target.last_seen_ms > target.last_report_ms + 10000; }, 35000);
      expect(expired.state).toBe('completed'); await expect(page.getByTestId('report-fresh-release-b')).toContainText('实际报告已过期或缺失');
      await snapshot(id + '-expired-report', { frozen, expired }); await capture(page, id + '-expired-report');
    } finally { signalFixture(pid, releaseProcessRoot + '/private/agent-b/artifacts/', 'SIGCONT'); }
    const recovered = await waitDeployment(page, id, d => d.targets.find((target: any) => target.identity_id === 'release-b').report_fresh);
    await expect(page.getByTestId('report-fresh-release-b')).toContainText('实际报告有效'); await snapshot(id + '-recovered-report', recovered); await capture(page, id + '-recovered-report');
  } finally {
    signalFixture(agent, releaseProcessRoot + '/private/agent-b', 'SIGCONT');
    const current = await api(page, '/release-deployments/' + id).catch(() => null);
    if (current?.status === 200) {
      const value = parseExactJSON(current.text) as any;
      if (!['completed', 'cancelled'].includes(value.state)) {
        const cancelled = await api(page, '/release-deployments/' + id + '/actions', { request_id: id + '-cleanup', expected_version: value.version, action: 'cancel', reason: '结束本次浏览器验证中尚未完成的临时批次' });
        await snapshot(id + '-test-cleanup', { before: value, response: cancelled });
      }
    }
  }
});
