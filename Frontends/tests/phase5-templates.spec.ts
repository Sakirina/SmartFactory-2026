import { test, expect, api, capture, choose, directory, get, login, route, snapshot } from './phase5-support';
import { createBatch, waitDeployment } from './phase5-release-helpers';
import { restartReleaseAgent } from './phase5-fixture-controls';
import { readFile } from 'node:fs/promises';
import { parseExactJSON } from '../src/precision';
async function addTag(page: any, label: string, value: string) {
  const input = page.getByRole('combobox', { name: label, exact: true }); await input.click(); await input.fill(value); await input.press('Enter'); await input.press('Escape');
  await expect(page.locator('.ant-form-item').filter({ has: input })).toContainText(value);
}
test('five existing template instances evolve with fixed bindings, conflict feedback and actual node publication', async ({ page }) => {
  test.skip(process.env.SF_PHASE5_CASES !== 'templates'); test.setTimeout(180000);
  const prerequisites = JSON.parse(await readFile(directory + '/five-template-prerequisites.json', 'utf8'));
  const nonce = Date.now(), instances: any[] = [];
  await login(page); await route(page, 'scene-templates');
  for (const item of prerequisites.devices) {
    const card = page.locator('.definition-grid .ant-card').filter({ has: page.getByText(item.template.name, { exact: true }) });
    await card.getByRole('button', { name: '使用此模板', exact: true }).click();
    const id = 'frontend-' + item.template.id + '-' + nonce, name = '页面演进' + nonce + '·' + item.template.name;
    await page.getByLabel('实例身份', { exact: true }).fill(id); await page.getByLabel('实例名称', { exact: true }).fill(name);
    await choose(page, '模板绑定设备', item.entity.name); await choose(page, '模板绑定配置', item.configuration.id);
    await page.getByRole('button', { name: '加入待准备批次', exact: true }).click();
    instances.push({ id, name, item });
  }
  let response = page.waitForResponse(value => value.request().method() === 'POST' && value.url().endsWith('/template-batches'));
  await page.getByRole('button', { name: '检查冲突并批量准备草稿', exact: true }).click();
  let saved = await response; expect(saved.status()).toBe(200); const original = parseExactJSON(await saved.text()) as any;
  expect(original.status).toBe('prepared'); expect(original.drafts).toHaveLength(25); expect(original.bindings).toHaveLength(5);
  await snapshot('five-template-prepared', original); await capture(page, 'five-template-prepared');
  response = page.waitForResponse(value => value.request().method() === 'POST' && value.url().endsWith('/template-batches'));
  await page.getByRole('button', { name: /检查冲突并批量准备草稿$/ }).click(); saved = await response;
  const failed = parseExactJSON(await saved.text()) as any; expect(saved.status()).toBe(200); expect(failed.status).toBe('failed'); expect(failed.drafts ?? []).toHaveLength(0);
  await expect(page.getByText('此批次保存了失败结果', { exact: true })).toBeVisible();
  response = page.waitForResponse(value => value.request().method() === 'POST' && value.url().endsWith('/retry'));
  await page.getByRole('button', { name: '外部条件修复后重试原批次', exact: true }).click(); saved = await response;
  const retried = parseExactJSON(await saved.text()) as any; expect(retried.attempts).toHaveLength(2); expect(retried.status).toBe('failed'); expect(retried.drafts ?? []).toHaveLength(0);
  await snapshot('template-source-conflicts', { original, failed, retried }); await capture(page, 'template-source-conflicts');
  await page.getByLabel('原模板批次身份', { exact: true }).fill(original.id); await page.getByRole('button', { name: '读取原实例与配置绑定', exact: true }).click();
  await expect(page.getByTestId('template-evolution')).toContainText(original.id);
  await page.getByLabel('模板演进身份', { exact: true }).fill('frontend-evolution-five-' + nonce);
  const releaseID = 'frontend-template-five-' + nonce;
  await page.getByLabel('模板固定发布身份', { exact: true }).fill(releaseID); await page.getByLabel('模板固定发布名称', { exact: true }).fill('五类模板沿用原设备和配置');
  await choose(page, '模板发布程序工件', 'r02-final-v2');
  for (const instance of instances) await page.getByRole('checkbox', { name: '演进实例 ' + instance.id, exact: true }).check();
  const climate = instances.find(value => value.item.template.id === 'climate-ventilation');
  await page.getByLabel(climate.name + '：高温恢复值（°C）', { exact: true }).fill('35');
  response = page.waitForResponse(value => value.request().method() === 'POST' && value.url().endsWith('/evolve'));
  await page.getByRole('button', { name: '检查参数并生成模板固定发布', exact: true }).click(); saved = await response; expect(saved.status()).toBe(400);
  await expect(page.getByText('模板演进未完成，当前参数继续保留', { exact: true })).toBeVisible();
  await expect(page.getByLabel(climate.name + '：高温恢复值（°C）', { exact: true })).toHaveValue('35');
  await snapshot('template-parameter-rejection', { status: saved.status(), response: parseExactJSON(await saved.text()) }); await capture(page, 'template-parameter-rejection');
  await page.getByLabel(climate.name + '：高温恢复值（°C）', { exact: true }).fill('27');
  await page.getByLabel(climate.name + '：温湿度平均窗口（ms）', { exact: true }).fill('45000');
  const goods = instances.find(value => value.item.template.id === 'goods-counting');
  await page.getByLabel(goods.name + '：计数窗口，0 表示累计全部有效观测（ms）', { exact: true }).fill('120000');
  response = page.waitForResponse(value => value.request().method() === 'POST' && value.url().endsWith('/evolve'));
  await page.getByRole('button', { name: /检查参数并生成模板固定发布$/ }).click(); saved = await response; expect(saved.status()).toBe(200);
  const evolved = parseExactJSON(await saved.text()) as any; expect(evolved.instances).toHaveLength(5); expect(evolved.bindings).toEqual(original.bindings);
  expect(evolved.instances.every((instance: any) => instance.template_version === 2)).toBe(true);
  expect(evolved.definition_ids).toHaveLength(25); expect(evolved.node_ids).toEqual(['edge-a']);
  expect(evolved.release.manifest.template.batch_id).toBe(original.id); expect(evolved.release.manifest.components).toHaveLength(29);
  await snapshot('five-template-evolved', evolved); await capture(page, 'five-template-evolved');
  for (const instance of instances.filter(value => value !== goods)) await page.getByRole('checkbox', { name: '演进实例 ' + instance.id, exact: true }).uncheck();
  await page.getByLabel('模板演进身份', { exact: true }).fill('frontend-evolution-partial-' + nonce); await page.getByLabel('模板固定发布身份', { exact: true }).fill('frontend-template-partial-' + nonce);
  response = page.waitForResponse(value => value.request().method() === 'POST' && value.url().endsWith('/evolve'));
  await page.getByRole('button', { name: /检查参数并生成模板固定发布$/ }).click(); saved = await response; expect(saved.status()).toBe(200);
  const partial = parseExactJSON(await saved.text()) as any; expect(partial.instances.map((value: any) => value.id)).toEqual([goods.id]);
  expect(partial.bindings).toEqual(original.bindings.filter((value: any) => value.instance_id === goods.id));
  expect((await get(page, '/template-batches/' + original.id)).input).toEqual(original.input); await snapshot('template-partial-fixed-bindings', partial); await capture(page, 'template-partial-fixed-bindings');
  await route(page, 'workload-identities');
  const beforeIdentity = (await get<any[]>(page, '/workload-identities')).find(value => value.id === 'release-a');
  await page.getByRole('row').filter({ hasText: 'release-a' }).first().getByRole('button', { name: '编辑范围或停用', exact: true }).click();
  for (const capability of ['connector:modbus_tcp', 'connector:opcua']) if (!beforeIdentity.capabilities.includes(capability)) await addTag(page, '允许的能力', capability);
  for (const id of [...new Set<string>(prerequisites.devices.map((item: any) => item.configuration.id))]) if (!beforeIdentity.connector_ids.includes(id)) await addTag(page, '连接器固定引用范围', id);
  await page.getByRole('button', { name: '保存身份范围', exact: true }).click(); await expect(page.getByRole('dialog', { name: '编辑工作负载身份' })).toBeHidden();
  const authorized = (await get<any[]>(page, '/workload-identities')).find(value => value.id === 'release-a');
  const restarted = await restartReleaseAgent('a'); await snapshot('template-node-current-scope', { before: beforeIdentity, after: authorized, restarted });
  const deploymentID = 'frontend-template-deployment-' + nonce;
  await createBatch(page, deploymentID, releaseID, [['release-a']], '五类模板参数已核对，沿用原绑定并在实际节点启用');
  const completed = await waitDeployment(page, deploymentID, value => value.state === 'completed' && value.targets[0].state === 'running' && value.targets[0].report_fresh, 65000);
  expect(completed.targets[0].runtime.program_sha256).toBe('b02566d928858835502d2d0cd6aac04ef561335e0498f7675fb1d60b0e71f229');
  expect(completed.targets[0].runtime.components).toHaveLength(29);
  for (const component of completed.targets[0].runtime.components) {
    const fixed = evolved.release.manifest.components.find((item: any) => item.id === component.id);
    expect(component.sha256).toBe(fixed.sha256); expect(component.version).toBe(fixed.version);
    if (component.kind === 'rule') { expect(component.applied_version).toBeGreaterThan(0); expect(component.applied_sha256).toMatch(/^[a-f0-9]{64}$/); }
    else expect(component.applied_sha256).toBe(component.sha256);
  }
  const consumer = await (await fetch('http://127.0.0.1:60057/runtime')).json();
  const token = (await readFile('/private/tmp/smartfactory-phase5-release-process-tenth-20261005/private/agent-a/runtime-token', 'utf8')).trim();
  const runtime = await (await fetch('http://127.0.0.1:60050/internal/releases/runtime', { headers: { Authorization: 'Bearer ' + token } })).json();
  const applied = consumer.connectors.filter((item: any) => item.connector_id.startsWith('frontend-five-'));
  expect(applied.map((item: any) => item.protocol).sort()).toEqual(['modbus_tcp', 'mqtt_device', 'opcua']);
  for (const item of applied) {
    const actual = runtime.configurations.find((value: any) => value.reference.id.endsWith('/' + item.connector_id));
    expect(actual.reference.version).toBe(1); expect(item.state.found).toBe(true);
    expect(item.state.applied_entity_revision).toBe(actual.apply_generation);
    expect(item.state.public_configuration_sha256).toBe(actual.consumer_digest);
  }
  await snapshot('five-template-actual-consumer', { consumer, runtime });
  await snapshot('five-template-real-node-running', completed); await capture(page, 'five-template-real-node-running');
  const { writeFile } = await import('node:fs/promises');
  await writeFile(directory + '/template-final-evolution.state.json', JSON.stringify(evolved, null, 2));
});
