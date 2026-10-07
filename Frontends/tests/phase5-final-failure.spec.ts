import { test, expect, api, capture, directory, get, login, snapshot, choose } from './phase5-support';
import { createBatch, waitDeployment } from './phase5-release-helpers';
import { readFile, writeFile, mkdir, chmod, stat } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { parseExactJSON } from '../src/precision';

test('final program startup failure stops the later batch and a restored key permits formal retry and compatible rollback', async ({ page }) => {
  test.skip(process.env.SF_PHASE5_CASES !== 'failure-rollback'); test.setTimeout(240000);
  await login(page);
  const fixture = JSON.parse(await readFile('/private/tmp/smartfactory-release-restore-recovery-20261005/frontend-fixture.json', 'utf8'));
  const source = JSON.parse(await readFile(directory + '/template-final-evolution.state.json', 'utf8'));
  const normal = JSON.parse(await readFile(directory + '/normal-final-release.state.json', 'utf8'));
  const nonce = Date.now(), id = 'frontend-failed-r02-' + nonce;
  const manifest = { ...source.release.manifest, id: 'frontend-failure-source-r02-' + nonce, name: '最终程序启动失败的双节点固定清单', template: undefined, components: [...source.release.manifest.components, ...normal.manifest.components.filter((item: any) => item.kind !== 'program')] };
  const checked = await api(page, '/releases/validate', manifest); await snapshot('failure-source-validation', { manifest, response: checked });
  expect(checked.status).toBe(200); expect((parseExactJSON(checked.text) as any).valid).toBe(true);
  const registered = await api(page, '/releases', { request_id: manifest.id, manifest }); expect(registered.status).toBe(200);
  await snapshot('failure-fixed-source', { validation: parseExactJSON(checked.text), registered: parseExactJSON(registered.text) }); await page.reload();
  const keyPath = fixture.original_master_key_file, original = await readFile(keyPath), permissions = (await stat(keyPath)).mode & 0o777;
  const digest = (value: Uint8Array) => createHash('sha256').update(value).digest('hex');
  await mkdir(directory + '/private', { recursive: true, mode: 0o700 });
  const privateBackup = directory + '/private/failure-original-master-' + nonce; await writeFile(privateBackup, original, { mode: 0o600 });
  let failed: any;
  try {
    await writeFile(keyPath, Buffer.from('invalid-key-for-controlled-child-startup'), { mode: permissions });
    await createBatch(page, id, manifest.id, [['release-a'], ['release-b']], '核对最终子程序启动失败、正式失败回执及后续批次停止');
    failed = await waitDeployment(page, id, value => value.state === 'failed' && value.targets[0].state === 'failed', 40000);
    expect(failed.targets[0].prepared_sha256).toBe(failed.release_sha256); expect(failed.targets[0].failure_component).toBe('program');
    expect(failed.targets[1].state).toBe('pending'); expect(failed.batches[1].entered_ms).toBe(0); expect(failed.batches[1].completed_ms).toBe(0);
    const reports = await get<any[]>(page, '/release-deployments/' + id + '/reports');
    expect(reports.some(value => value.state === 'prepared' && value.generation === failed.targets[0].generation)).toBe(true);
    expect(reports.some(value => value.state === 'failed' && value.component_id === 'program' && /application exited/.test(value.reason))).toBe(true);
    await snapshot('application-failed-later-batch-stopped', { failed, reports, private_backup: privateBackup, original_key_sha256: digest(original) }); await capture(page, 'application-failed-later-batch-stopped');
  } finally {
    await writeFile(keyPath, original); await chmod(keyPath, permissions);
    expect(digest(await readFile(keyPath))).toBe(digest(original)); expect((await stat(keyPath)).mode & 0o777).toBe(permissions);
    await snapshot('controlled-key-restored', { key_file: keyPath, private_backup: privateBackup, restored_sha256: digest(await readFile(keyPath)), permissions });
  }
  await page.getByRole('button', { name: '核对状态并更新操作依据', exact: true }).click();
  await page.getByLabel('发布处理原因', { exact: true }).fill('主密钥已按原始字节与权限恢复，正式重试当前失败批次');
  let response = page.waitForResponse(value => value.request().method() === 'POST' && value.url().endsWith('/actions'));
  await page.getByRole('button', { name: '重新尝试', exact: true }).click(); let received = await response; expect(received.status()).toBe(200);
  const retry = parseExactJSON(await received.text()) as any; expect(retry.targets[0].generation).toBeGreaterThan(failed.targets[0].generation);
  const running = await waitDeployment(page, id, value => value.state === 'completed' && value.targets.every((target: any) => target.state === 'running' && target.report_fresh), 65000);
  expect(running.batches[1].entered_ms).toBeGreaterThan(0); for (const target of running.targets) expect(target.runtime.program_sha256).toBe(fixture.runtime.program_sha256);
  await snapshot('manual-retry-actual-running', { failed, retry, running, reports: await get(page, '/release-deployments/' + id + '/reports') }); await capture(page, 'manual-retry-actual-running');
  await page.getByRole('button', { name: '核对状态并更新操作依据', exact: true }).click();
  await page.getByLabel('发布处理原因', { exact: true }).fill('数据库迁移11先核对旧版本兼容条件，再选择受支持的固定发布');
  await choose(page, '退回的固定发布', 'release-legacy'); await page.getByLabel('退回批次身份', { exact: true }).fill('frontend-incompatible-r02-' + nonce);
  response = page.waitForResponse(value => value.request().method() === 'POST' && value.url().endsWith('/rollback'));
  await page.getByRole('button', { name: '退回旧发布', exact: true }).click(); received = await response; expect(received.status()).toBe(400);
  const incompatibility = parseExactJSON(await received.text()) as any;
  await expect(page.getByText('发布处理未完成', { exact: true })).toBeVisible(); expect(JSON.stringify(incompatibility)).toContain('migration');
  await expect(page.getByLabel('发布处理原因', { exact: true })).toHaveValue('数据库迁移11先核对旧版本兼容条件，再选择受支持的固定发布');
  await snapshot('incompatible-rollback-recovery', incompatibility); await capture(page, 'incompatible-rollback-recovery');
  await choose(page, '退回的固定发布', 'recovery-r02-final-original-v1');
  const rollbackID = 'frontend-compatible-r02-rollback-' + nonce; await page.getByLabel('退回批次身份', { exact: true }).fill(rollbackID);
  response = page.waitForResponse(value => value.request().method() === 'POST' && value.url().endsWith('/rollback'));
  await page.getByRole('button', { name: '退回旧发布', exact: true }).click(); received = await response; expect(received.status()).toBe(200);
  const completed = await waitDeployment(page, rollbackID, value => value.state === 'completed' && value.targets.every((target: any) => target.report_fresh && target.state === 'running'), 65000);
  expect(completed.rollback_of).toBe(id); const old = await get(page, '/releases/recovery-r02-final-original-v1'); const program = old.manifest.components.find((component: any) => component.kind === 'program');
  for (const target of completed.targets) { expect(target.running_sha256).toBe(old.sha256); expect(target.runtime.program_sha256).toBe(program.sha256); expect(target.runtime.migration_version).toBe(11); }
  await snapshot('compatible-rollback-actual-running', { after: completed, source: old }); await capture(page, 'compatible-rollback-actual-running');
  await createBatch(page, 'frontend-rollback-r02-recovery-' + nonce, normal.id, [['release-a'], ['release-b']], '退回验证完成，恢复最终组合程序及固定配置');
  const restored = await waitDeployment(page, 'frontend-rollback-r02-recovery-' + nonce, value => value.state === 'completed' && value.targets.every((target: any) => target.report_fresh), 65000);
  await snapshot('rollback-restored-final-program', restored); await capture(page, 'rollback-restored-final-program');
});
