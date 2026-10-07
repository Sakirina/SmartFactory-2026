import { test, expect, capture, get, login, route, snapshot } from './phase5-support';
test('preserve current release and identity checkpoint before backend repair', async ({ page }) => {
  test.skip(process.env.SF_PHASE5_CASES !== 'checkpoint');
  await login(page);
  const deployments = await get<any[]>(page, '/release-deployments');
  const current = deployments.find(item => item.id === 'frontend-failed-1791192514777'); expect(current).toBeTruthy();
  await route(page, 'releases?deployment=' + current.id);
  await expect(page.getByTestId('release-detail')).toContainText(current.id);
  await snapshot('current-release-before-backend-repair', { current, deployments, identities: await get(page, '/workload-identities'), reports: await get(page, '/configuration-reports') }); await capture(page, 'current-release-before-backend-repair');
});
