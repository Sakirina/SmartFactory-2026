import { readFile } from 'node:fs/promises';
import { test, expect, api, capture, choose, directory, get, login, route, snapshot } from './phase5-support';
import { parseExactJSON } from '../src/precision';
test('template business-version refresh preserves newly typed parameters and permits actual evolution submission', async ({ page, context }) => {
  test.skip(!['template-basis-before', 'template-basis-fixed'].includes(process.env.SF_PHASE5_CASES || '')); test.setTimeout(90000);
  const before = process.env.SF_PHASE5_CASES === 'template-basis-before';
  const prerequisites = JSON.parse(await readFile(directory + '/five-template-prerequisites.json', 'utf8'));
  const item = prerequisites.devices.find((value: any) => value.template.id === 'goods-counting');
  const nonce = Date.now(), instanceID = 'frontend-delay-instance-' + nonce, deviceID = 'frontend-delay-device-' + nonce, instanceName = '模板延迟刷新验证 ' + nonce;
  await login(page);
  const created = await api(page, '/template-batches', { id: 'frontend-delay-source-' + nonce, request_id: 'frontend-delay-source-' + nonce, group_id: 'factory', instances: [{ id: instanceID, name: instanceName, template_id: item.template.id, template_version: 1, device_id: deviceID, device_version: 1, configuration_id: item.configuration.id, configuration_version: item.configuration.version, safety_user_id: 'admin', parameters: { mode: 'delta' } }] });
  expect(created.status).toBe(200); const original = parseExactJSON(created.text) as any; expect(original.status).toBe('failed'); expect(original.version).toBe(1);
  await route(page, 'scene-templates'); await page.getByLabel('原模板批次身份', { exact: true }).fill(original.id);
  await page.getByRole('button', { name: '读取原实例与配置绑定', exact: true }).click();
  await expect(page.getByTestId('template-evolution')).toContainText(original.id);
  await page.getByLabel('模板演进身份', { exact: true }).fill('frontend-delay-evolution-' + nonce);
  await page.getByLabel('模板固定发布身份', { exact: true }).fill('frontend-delay-release-' + nonce);
  await page.getByLabel('模板固定发布名称', { exact: true }).fill('刷新开始前的模板发布名称');
  await choose(page, '模板发布程序工件', 'r02-final-v2');
  await page.getByRole('checkbox', { name: '演进实例 ' + instanceID, exact: true }).check();
  const edgePage = await context.newPage(); await login(edgePage, 'release-edge', 'edge.html');
  const device = { ...item.entity, id: deviceID, name: '延迟刷新条件修复设备 ' + nonce, version: 1, config: { ...item.entity.config, deviceConfig: { ...item.entity.config.deviceConfig, deviceId: deviceID, deviceName: '延迟刷新条件修复设备 ' + nonce } } };
  const fixedDevice = await api(edgePage, '/entities', { entity: device, expected_version: 0 }); expect(fixedDevice.status).toBe(200);
  await expect.poll(async () => (await get<any[]>(page, '/entities')).some(value => value.id === deviceID), { timeout: 45000, intervals: [500, 1000] }).toBe(true);
  const retried = await api(page, '/template-batches/' + original.id + '/retry', { expected_version: original.version, request_id: 'frontend-delay-retry-' + nonce });
  expect(retried.status).toBe(200); const current = parseExactJSON(retried.text) as any;
  await snapshot('template-basis-precondition', { original, current, device });
  expect(current.status).toBe('prepared'); expect(current.version).toBe(2);
  // Remount with the retained v1 edit while the server supplies the v2 source.
  await route(page, 'releases'); await page.getByRole('button', { name: '组合固定发布', exact: true }).waitFor();
  await route(page, 'scene-templates'); await page.getByLabel('原模板批次身份', { exact: true }).fill(original.id);
  await page.getByRole('button', { name: '读取原实例与配置绑定', exact: true }).click();
  await expect(page.getByRole('button', { name: '检查参数并生成模板固定发布', exact: true })).toBeEnabled();
  let response = page.waitForResponse(value => value.request().method() === 'POST' && value.url().endsWith('/evolve'));
  await page.getByRole('button', { name: '检查参数并生成模板固定发布', exact: true }).click(); expect((await response).status()).toBe(409);
  let releaseResponse!: () => void, started!: () => void, finished!: () => void;
  const delay = new Promise<void>(resolve => { releaseResponse = resolve; }), began = new Promise<void>(resolve => { started = resolve; }), fulfilled = new Promise<void>(resolve => { finished = resolve; }); let armed = true;
  const pattern = '**/api/sf/v1/template-batches/' + original.id;
  await page.route(pattern, async request => { if (armed && request.request().method() === 'GET') { armed = false; const response = await request.fetch(); started(); await delay; await request.fulfill({ response }); finished(); } else await request.continue(); });
  await page.getByRole('button', { name: '核对原批次并更新已阅版本', exact: true }).click(); await began;
  await page.getByLabel('模板固定发布名称', { exact: true }).fill('HTTP响应等待期间新输入的模板发布名称');
  await page.getByLabel(instanceName + '：计数窗口，0 表示累计全部有效观测（ms）', { exact: true }).fill('180000');
  releaseResponse(); await fulfilled; await page.unroute(pattern);
  const name = await page.getByLabel('模板固定发布名称', { exact: true }).inputValue();
  const window = await page.getByLabel(instanceName + '：计数窗口，0 表示累计全部有效观测（ms）', { exact: true }).inputValue();
  await snapshot('template-delayed-refresh', { original, current, name, window, expected_source_version: 2 }); await capture(page, 'template-delayed-refresh');
  if (before) { expect(name).toBe('刷新开始前的模板发布名称'); expect(window).toBe('0'); }
  else {
    expect(name).toBe('HTTP响应等待期间新输入的模板发布名称'); expect(window).toBe('180000');
    const submit = page.getByRole('button', { name: /检查参数并生成模板固定发布$/ });
    await snapshot('template-final-submit-control', { html: await submit.evaluate(element => element.outerHTML), at_ms: Date.now() });
    await expect(submit).not.toHaveClass(/ant-btn-loading/);
    response = page.waitForResponse(value => value.request().method() === 'POST' && value.url().endsWith('/evolve'));
    await submit.click(); const saved = await response; expect(saved.status()).toBe(200);
    const evolved = parseExactJSON(await saved.text()) as any; expect(evolved.source_batch_version).toBe(2); expect(evolved.release.manifest.name).toBe(name); expect(evolved.instances[0].parameters.count_window_ms).toBe(180000);
    await snapshot('template-new-input-actual-submitted', evolved); await capture(page, 'template-new-input-actual-submitted');
  }
  await edgePage.close();
});
