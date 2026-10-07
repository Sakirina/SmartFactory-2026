import { readFile } from 'node:fs/promises';
import { test, expect, login, route, get, choose, capture, snapshot, directory } from './phase5-support';
import { restartReleaseAgent } from './phase5-fixture-controls';

test('recovery fixtures retain original identity and persistent state with the final agent and normal manifest', async ({ page }) => {
  test.skip(process.env.SF_PHASE5_CASES !== 'resume'); test.setTimeout(90000);
  const fixture = JSON.parse(await readFile('/private/tmp/smartfactory-release-restore-recovery-20261005/frontend-fixture.json', 'utf8'));
  await login(page); await route(page, 'releases?deployment=' + fixture.deployment.id);
  await expect(page.getByTestId('release-detail').locator('.ant-card-head-title')).toContainText(fixture.deployment.id);
  await expect(page.getByTestId('report-fresh-release-a')).toContainText('实际报告有效');
  const beforeIdentity = (await get<any[]>(page, '/workload-identities')).find(value => value.id === 'release-b');
  const beforeDeployment = await get(page, '/release-deployments/' + fixture.deployment.id);
  const restarted = await restartReleaseAgent('b', fixture.final_agent_binary);
  await expect.poll(async () => (await get<any[]>(page, '/workload-identities')).find(value => value.id === 'release-b').instance_epoch, { timeout: 15000 }).toBeGreaterThan(beforeIdentity.instance_epoch);
  const afterIdentity = (await get<any[]>(page, '/workload-identities')).find(value => value.id === 'release-b');
  expect(afterIdentity.generation).toBe(beforeIdentity.generation);
  expect(afterIdentity.connector_ids).toEqual(beforeIdentity.connector_ids);
  await snapshot('b-final-agent-replacement', { beforeIdentity, afterIdentity, beforeDeployment, restarted });
  for (const failed of (await get<any[]>(page, '/release-deployments')).filter(value => value.state === 'failed' && value.targets?.some((target: any) => ['release-a', 'release-b'].includes(target.identity_id)))) {
    await route(page, 'releases?deployment=' + failed.id);
    await expect(page.getByTestId('release-detail').locator('.ant-card-head-title')).toContainText(failed.id);
    await page.getByLabel('发布处理原因', { exact: true }).fill('保留原后端失败记录，开始修正版页面组合验证');
    await page.getByRole('button', { name: '取消后续发布', exact: true }).click();
    await expect.poll(async () => (await get(page, '/release-deployments/' + failed.id)).state).toBe('cancelled');
    await snapshot('historical-failed-batch-cancelled', { before: failed, after: await get(page, '/release-deployments/' + failed.id) });
  }
  await route(page, 'releases'); await page.getByRole('button', { name: '组合固定发布', exact: true }).click();
  await choose(page, '参考已有固定清单', 'recovery-r02-final-original-v1');
  const id = 'frontend-normal-r02-v2-' + Date.now();
  await page.getByLabel('固定发布身份', { exact: true }).fill(id);
  await page.getByLabel('固定发布名称', { exact: true }).fill('最终恢复程序的双节点固定发布');
  await choose(page, '已登记程序工件', 'r02-final-v2');
  await page.getByRole('button', { name: '校验依赖与兼容', exact: true }).click();
  await expect(page.getByText('依赖与兼容校验通过', { exact: true })).toBeVisible();
  await capture(page, 'r02-final-normal-validation');
  await page.getByRole('button', { name: '登记固定发布', exact: true }).click();
  await expect(page.getByRole('dialog', { name: '组合程序、规则和配置' })).toBeHidden();
  const saved = await get(page, '/releases/' + id);
  expect(saved.manifest.components.find((item: any) => item.kind === 'program').sha256).toBe(fixture.runtime.program_sha256);
  await snapshot('r02-final-normal-record', saved);
  const { writeFile } = await import('node:fs/promises');
  await writeFile(directory + '/normal-final-release.state.json', JSON.stringify(saved, null, 2));
  await capture(page, 'r02-final-normal-record');
});
