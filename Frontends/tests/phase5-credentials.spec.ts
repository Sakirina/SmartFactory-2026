import { test, expect, capture, get, login, route, snapshot, directory } from './phase5-support';
import { nodeProcessRoot, restartNodeConsumer } from './phase5-fixture-controls';
import { readFile, writeFile } from 'node:fs/promises';
import { randomBytes } from 'node:crypto';
import https from 'node:https';
async function policy() { const result = await fetch('http://127.0.0.1:60904/runtime', { signal: AbortSignal.timeout(3000) }); return result.json(); }
async function identity(page: any) { return (await get<any[]>(page, '/workload-identities')).find(item => item.id === 'workload-a'); }
async function setEnabled(page: any, enabled: boolean) {
  await page.getByRole('row').filter({ hasText: 'workload-a' }).first().getByRole('button', { name: '编辑范围或停用', exact: true }).click();
  await page.getByRole('switch', { name: '启用身份', exact: true }).setChecked(enabled);
  await page.getByRole('button', { name: '保存身份范围', exact: true }).click(); await expect(page.getByRole('dialog', { name: '编辑工作负载身份' })).toBeHidden();
}
async function session(options: any, credential: string) {
  const material = { ca: await readFile(options.tls_ca), cert: await readFile(options.tls_certificate), key: await readFile(options.tls_key) };
  return new Promise<{ status: number; response: string }>((resolve, reject) => {
    const request = https.request(options.config_url + '/internal/workloads/session', { ...material, method: 'POST', headers: { Authorization: 'Bearer ' + credential, 'Content-Type': 'application/json' } }, response => { let body = ''; response.on('data', chunk => { body += chunk; }); response.on('end', () => resolve({ status: response.statusCode!, response: body })); });
    request.on('error', reject); request.end(JSON.stringify({ instance_id: 'old-credential-probe' }));
  });
}
test('disable and credential rotation clear prior node configuration and a new authenticated process restores actual values', async ({ page }) => {
  test.skip(process.env.SF_PHASE5_CASES !== 'credentials'); test.setTimeout(100000);
  await login(page, 'node'); await route(page, 'workload-identities'); await page.getByLabel('筛选节点身份', { exact: true }).fill('workload-a');
  const options = JSON.parse(await readFile(nodeProcessRoot + '/private/a.json', 'utf8'));
  let prior: any; await expect.poll(async () => { try { prior = await policy(); return true; } catch { return false; } }, { timeout: 15000 }).toBe(true);
  const before = await identity(page), oldCredential = (await readFile(options.token_file, 'utf8')).trim();
  let enabled = false;
  try {
    await setEnabled(page, false); await expect.poll(async () => (await policy()).policy.StartTTLMS, { timeout: 15000 }).toBe(10000);
    const disabled = await identity(page), revoked = await policy(); expect(disabled.enabled).toBe(false);
    const denied = await session(options, oldCredential); expect(denied.status).toBe(401);
    await snapshot('identity-disabled-cache-cleared', { before, disabled, before_policy: prior.policy, revoked_policy: revoked.policy, denied }); await capture(page, 'identity-disabled-cache-cleared');
    await setEnabled(page, true); enabled = true; const restarted = await restartNodeConsumer('a');
    await expect.poll(async () => (await policy()).policy.StartTTLMS, { timeout: 15000 }).toBe(prior.policy.StartTTLMS);
    const recovered = await identity(page); expect(recovered.instance_epoch).toBeGreaterThan(before.instance_epoch);
    await snapshot('identity-enabled-new-process', { recovered, restarted, actual: (await policy()).policy }); await capture(page, 'identity-enabled-new-process');
    await page.getByRole('row').filter({ hasText: 'workload-a' }).first().getByRole('button', { name: '轮换私有凭据', exact: true }).click();
    const newCredential = randomBytes(40).toString('hex'); await writeFile(directory + '/private/rotated-node-a-credential', newCredential, { mode: 0o600 });
    await page.getByLabel('新的私有工作负载凭据', { exact: true }).fill('too-short'); await expect(page.getByRole('button', { name: '确认轮换凭据', exact: true })).toBeDisabled();
    await page.getByLabel('新的私有工作负载凭据', { exact: true }).fill(newCredential); await expect(page.getByLabel('新的私有工作负载凭据', { exact: true })).toHaveAttribute('type', 'password');
    await page.getByRole('button', { name: '确认轮换凭据', exact: true }).click(); await expect(page.getByRole('dialog', { name: '轮换工作负载私有凭据' })).toBeHidden();
    const rotated = await identity(page); expect(rotated.generation).toBeGreaterThan(recovered.generation);
    await expect.poll(async () => (await policy()).policy.StartTTLMS, { timeout: 15000 }).toBe(10000);
    const oldDenied = await session(options, oldCredential); expect(oldDenied.status).toBe(401);
    await snapshot('credential-rotated-old-instance-cleared', { recovered, rotated, old_credential_denied: oldDenied, actual: (await policy()).policy }); await capture(page, 'credential-rotated-old-instance-cleared');
    await writeFile(options.token_file, newCredential, { mode: 0o600 }); const activated = await restartNodeConsumer('a');
    await expect.poll(async () => (await policy()).policy.StartTTLMS, { timeout: 15000 }).toBe(prior.policy.StartTTLMS);
    const current = await identity(page); expect(current.instance_epoch).toBeGreaterThan(rotated.instance_epoch);
    const reports = (await get<any[]>(page, '/configuration-reports')).filter(item => item.identity_id === current.id);
    expect(reports.every(item => item.generation === current.generation)).toBe(true);
    expect(reports.some(item => item.id === 'control.start_ttl_ms' && item.effective_value === prior.policy.StartTTLMS && item.state === 'running')).toBe(true);
    expect(reports.filter(item => item.id === 'secret.a').every(item => item.effective_value === undefined)).toBe(true);
    await snapshot('rotated-credential-real-new-generation', { current, activated, reports, actual: (await policy()).policy }); await capture(page, 'rotated-credential-real-new-generation');
    expect(await page.locator('body').innerText()).not.toContain(newCredential);
  } finally { if (!enabled) { await setEnabled(page, true).catch(() => {}); await restartNodeConsumer('a').catch(() => {}); } }
});
