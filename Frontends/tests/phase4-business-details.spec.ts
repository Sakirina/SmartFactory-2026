import { test, expect, type Page } from '@playwright/test';
import { readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { root, evidence, phase, login, capture, route, jsonAPI } from './phase4-support';

async function choose(page: Page, label: string, name: string) {
  const field = page.getByLabel(label, { exact: true });
  await field.click(); await field.fill(name);
  const options=page.locator('.ant-select-dropdown:visible .ant-select-item-option-content'); await expect(options).toHaveText([name]); await field.press('ArrowDown'); await field.press('Enter');
}
async function alarmChange(page: Page, id: string, reason: string) {
  const current = JSON.parse((await jsonAPI(page, '/alarms/' + encodeURIComponent(id))).text);
  const response = await jsonAPI(page, '/alarms/' + encodeURIComponent(id) + '/actions', { request_id: crypto.randomUUID(), expected_version: current.case.version, expected_action_version: current.action_version, action: 'note', reason });
  expect(response.status).toBe(200); return JSON.parse(response.text);
}

test.afterEach(async({page},info)=>{if(info.status!==info.expectedStatus)await capture(page,'failure-'+info.title.slice(0,50).replace(/[^a-z0-9]+/gi,'-'));});

test('alarm version conflict retains reviewed basis and reason until explicit adoption', async ({ page }) => {
  test.skip(phase !== 'details'); await login(page); const manifest = JSON.parse(await readFile(path.join(root, 'cloud-final', 'manifest.json'), 'utf8'));
  await route(page, 'alarm-workbench?alarm=' + encodeURIComponent(manifest.alarm_id));
  await page.getByRole('button', { name: '添加备注', exact: true }).click();
  const modal = page.getByRole('dialog', { name: '添加备注', exact: true }); const reason = '版本冲突后继续保留的现场理由';
  await modal.getByLabel('处置理由').fill(reason); const reviewed = await modal.locator('.reviewed-version code').innerText();
  const other = await alarmChange(page, manifest.alarm_id, '另一次人工修改推进处置版本');
  let waiting = page.waitForResponse(response => response.request().method() === 'POST' && response.url().endsWith('/actions'));
  await modal.getByRole('button', { name: '提交处置', exact: true }).click(); const rejected = await waiting;
  expect(rejected.status()).toBe(409); await expect(modal.getByLabel('处置理由')).toHaveValue(reason);
  await expect(modal.locator('.reviewed-version code')).toHaveText(reviewed);
  const oldInput = JSON.parse(rejected.request().postData()!);
  await expect(page.getByRole('dialog',{name:'告警生命周期与处置'}).getByText('另一次人工修改推进处置版本',{exact:true}).first()).toBeVisible(); await modal.getByRole('button', { name: '采用更新后的告警依据', exact: true }).click(); await expect(modal.getByRole('button',{name:/提交处置$/})).not.toHaveClass(/ant-btn-loading/); await capture(page,'alarm-conflict-before-resubmit');
  waiting = page.waitForResponse(response => response.request().method() === 'POST' && response.url().endsWith('/actions'));
  await modal.getByRole('button', { name: /提交处置$/ }).click(); const saved = await waiting; expect(saved.status()).toBe(200);
  const newInput = JSON.parse(saved.request().postData()!); expect(newInput.reason).toBe(reason); expect(newInput.expected_version).toBeGreaterThan(oldInput.expected_version); expect(newInput.request_id).not.toBe(oldInput.request_id);
  await expect(modal).toBeHidden(); await expect(page.getByRole('button', { name: '完成处置', exact: true })).toBeDisabled();
  await writeFile(path.join(evidence, 'alarm-conflict-adoption.json'), JSON.stringify({ other, oldInput, newInput, saved: await saved.json() }, null, 2)); await capture(page, 'alarm-conflict-explicit-adoption');
});

test('handover changes allowed actor and the new owner completes the work order', async ({ page }) => {
  test.skip(phase !== 'details'); const handed = JSON.parse(await readFile(path.join(root, 'browser-business-final', 'handover-persisted.json'), 'utf8')); const id = handed.work_order.id;
  await login(page); await route(page, 'work-orders?work_order=' + encodeURIComponent(id));
  await expect(page.getByRole('button', { name: '办理交接', exact: true })).toBeDisabled(); await capture(page, 'work-order-former-owner-permission');
  await page.getByRole('dialog',{name:'工单责任与交接记录'}).getByRole('button',{name:'关闭',exact:true}).click(); await page.getByRole('button', { name: '退出', exact: true }).click(); await login(page, 'cloud', 'index.html', 'operator-b'); await route(page, 'work-orders?work_order=' + encodeURIComponent(id));
  await page.getByRole('button', { name: '完成工单', exact: true }).click(); const modal = page.getByRole('dialog', { name: '完成工单', exact: true });
  await modal.getByLabel('工单变更理由').fill('下一班次已完成温度与风机复核'); const waiting = page.waitForResponse(response => response.request().method() === 'POST' && response.url().endsWith('/actions'));
  await modal.getByRole('button', { name: '提交工单变更', exact: true }).click(); const response = await waiting; expect(response.status()).toBe(200);
  const saved = await response.json(); expect(saved.work_order.status).toBe('completed'); expect(saved.work_order.entries.at(-1).actor.user_id).toBe('operator-b');
  await writeFile(path.join(evidence, 'work-order-completed.json'), JSON.stringify(saved, null, 2)); await capture(page, 'work-order-new-owner-completed');
});

test('template conflict keeps zero drafts and retry appends a persisted attempt', async ({ page }) => {
  test.skip(phase !== 'details'); await login(page); const prepared = JSON.parse(await readFile(path.join(root, 'browser-business-final', 'templates-persisted.json'), 'utf8')); const original = prepared.input.instances[0];
  await route(page, 'scene-templates'); await page.locator('.ant-card').filter({ has: page.getByText('温湿度与通风', { exact: true }) }).first().getByRole('button', { name: '使用此模板' }).click();
  await page.getByLabel('实例身份', { exact: true }).fill(original.id);
  for (const label of ['模板绑定设备', '模板绑定配置']) { await page.getByLabel(label).click(); await page.getByLabel(label).press('ArrowDown'); await page.getByLabel(label).press('Enter'); }
  await page.getByRole('button', { name: '加入待准备批次', exact: true }).click(); let waiting = page.waitForResponse(response => response.request().method() === 'POST' && response.url().endsWith('/template-batches'));
  await page.getByRole('button', { name: '检查冲突并批量准备草稿', exact: true }).click(); const response = await waiting; expect(response.status()).toBe(200); const failed = await response.json();
  expect(failed.status).toBe('failed'); expect(failed.drafts || []).toHaveLength(0); await expect(page.getByText('此批次保存了失败结果', { exact: true })).toBeVisible();
  waiting = page.waitForResponse(response => response.request().method() === 'POST' && response.url().endsWith('/retry'));
  await page.getByRole('button', { name: '外部条件修复后重试原批次', exact: true }).click(); const retried = await waiting; expect(retried.status()).toBe(200); const repeated = await retried.json();
  expect(repeated.status).toBe('failed'); expect(repeated.drafts || []).toHaveLength(0); expect(repeated.attempts).toHaveLength(2); expect(repeated.version).toBeGreaterThan(failed.version);
  await writeFile(path.join(evidence, 'template-failed-retry.json'), JSON.stringify({ failed, repeated, external_conflict_preserved: true }, null, 2)); await capture(page, 'template-conflict-and-retry');
});

test('three protocol metadata forms validate required fields enums units and preserve numeric extensions', async ({ page }) => {
  test.skip(phase !== 'details'); await login(page, 'edge', 'edge.html'); const entities = JSON.parse((await jsonAPI(page, '/entities')).text); const saved = [];
  for (const id of ['device-climate-ventilation', 'device-infrared-lighting', 'device-hazardous-gas']) {
    await route(page, 'protocol-configurations'); const device = entities.find((entity: any) => entity.id === id); await choose(page, '参数设备', device.name);
    await expect(page.locator('.ant-descriptions').getByText(device.name, { exact: true })).toBeVisible(); await page.getByRole('button', { name: '编辑设备参数', exact: true }).click();
    const drawer = page.getByRole('dialog', { name: '设备基础参数编辑' }); await drawer.getByRole('tab', { name: '数据点', exact: true }).click(); const key = drawer.getByLabel(/^数据点 1：数据字段/); const oldKey = await key.inputValue();
    await key.fill(''); await drawer.getByRole('button', { name: /配置预检$/ }).click(); await expect(drawer.getByText('配置预检存在问题', { exact: true })).toBeVisible(); await key.fill(oldKey);
    const quality = drawer.getByLabel(/^数据点 1：质量/); await quality.click(); await expect(page.locator('.ant-select-dropdown:visible').getByText('UNCERTAIN', { exact: true })).toBeVisible(); await page.locator('.ant-select-dropdown:visible .ant-select-item-option-content').getByText('GOOD', { exact: true }).click();
    await drawer.getByRole('tab', { name: '采样', exact: true }).click(); await expect(drawer.getByLabel(/^采样周期（ms）/)).toBeVisible();
    await drawer.getByRole('tab', { name: '完整参数与扩展', exact: true }).click(); const raw = drawer.getByLabel('完整协议参数'); const value = JSON.parse(await raw.inputValue()); value.datapoints[0].extension_counter='__exact_counter__'; value.datapoints[0].extension_object={'保留':'未知扩展'}; await raw.fill(JSON.stringify(value,null,2).replace('"__exact_counter__"','9007199254740993'));
    await drawer.getByRole('button', { name: /配置预检$/ }).click(); await expect(drawer.getByText('配置预检通过', { exact: true })).toBeVisible();
    const waiting = page.waitForResponse(response => response.request().method() === 'POST' && response.url().endsWith('/device-configurations'));
    await drawer.getByRole('button', { name: /保存参数$/ }).click(); const response = await waiting; expect(response.status()).toBe(200); const result = await response.text(); expect(result).toMatch(/"extension_counter":9007199254740993/);
    await expect(drawer).toBeHidden(); await page.getByRole('button', { name: '编辑设备参数', exact: true }).click(); await drawer.getByRole('tab', { name: '完整参数与扩展', exact: true }).click(); await expect(raw).toHaveValue(/"extension_counter":\s*9007199254740993/);
    saved.push({ id, protocol: JSON.parse(result).configuration.protocol, response: result, integer_json_type_preserved: true }); await capture(page, 'protocol-roundtrip-' + id); await drawer.getByRole('button', { name: '关闭', exact: true }).click();
  }
  await writeFile(path.join(evidence, 'three-protocol-roundtrips.json'), JSON.stringify(saved, null, 2));
});

test('node metadata checks ranges units enums and exact extension before saving a draft', async ({ page }) => {
  test.skip(phase !== 'details'); await login(page); await route(page, 'alarm'); await page.getByRole('button', { name: '新建告警编排', exact: true }).click();
  const drawer = page.locator('.editor-drawer'); await choose(page,'选择数据设备','温湿度与通风'); await drawer.getByLabel('输入测点').fill('temperature'); await drawer.getByRole('button', { name: '延时与去抖', exact: true }).click(); await drawer.locator('.react-flow__node').filter({ hasText: '延时与去抖' }).click();
  const duration = drawer.getByLabel('等待时间（ms）', { exact: true }); await duration.fill('86400001'); await drawer.getByRole('button', { name: '应用基础参数', exact: true }).click(); await expect(drawer.getByText('等待时间超过最大值 86400000', { exact: true })).toBeVisible();
  await duration.fill('1500'); await drawer.getByLabel('延时模式', { exact: true }).click(); await page.locator('.ant-select-dropdown:visible .ant-select-item-option-content').getByText('activation', { exact: true }).click(); await drawer.getByRole('button', { name: '应用基础参数', exact: true }).click();
  await drawer.getByRole('tab', { name: '完整参数与扩展', exact: true }).click(); await drawer.getByLabel('节点完整参数').fill('{"duration_ms":1500,"mode":"activation","extension_counter":9007199254740993}'); await drawer.getByRole('button', { name: '应用基础参数', exact: true }).click();
  await drawer.getByRole('button',{name:'阈值判断',exact:true}).click(); await drawer.locator('.react-flow__node').filter({hasText:'阈值判断'}).click(); await drawer.getByLabel('判断值',{exact:true}).fill('null'); await drawer.getByRole('button',{name:'应用基础参数',exact:true}).click(); await expect(drawer.getByText(/判断值.*必填/)).toBeVisible(); await drawer.getByLabel('判断值',{exact:true}).fill('9007199254740993'); await drawer.getByRole('button',{name:'应用基础参数',exact:true}).click();
  const waiting = page.waitForResponse(response => response.request().method() === 'POST' && response.url().endsWith('/drafts')); await drawer.getByRole('button', { name: '保存草稿', exact: true }).click(); const response = await waiting; expect(response.status()).toBe(200); const raw = await response.text(); expect(raw).toMatch(/"extension_counter":9007199254740993/); expect(raw).toMatch(/"value":9007199254740993/);
  await writeFile(path.join(evidence, 'node-metadata-draft.json'), raw); await capture(page, 'node-metadata-exact-save');
});

test('draft polling and version conflict retain current editing through explicit version refresh', async ({ page }) => {
  test.skip(phase !== 'details'); await login(page); await route(page, 'analysis'); await page.locator('.draft-list button').first().click(); const drawer = page.locator('.editor-drawer'); const name = drawer.getByLabel('定义名称');
  const originalName = await name.inputValue(); const original = JSON.parse((await jsonAPI(page, '/drafts')).text).find((draft: any) => draft.definition.name === originalName);
  await name.fill('后台修改之后仍保留的未提交编辑'); const other = await jsonAPI(page, '/drafts', { draft: { ...original, definition: { ...original.definition, name: '另一处保存的版本' } }, expected_version: original.version }); expect(other.status).toBe(200);
  await page.waitForTimeout(1100); await expect(name).toHaveValue('后台修改之后仍保留的未提交编辑'); const waiting = page.waitForResponse(response => response.request().method() === 'POST' && response.url().endsWith('/drafts'));
  await drawer.getByRole('button', { name: '保存草稿', exact: true }).click(); expect((await waiting).status()).toBe(409); await expect(name).toHaveValue('后台修改之后仍保留的未提交编辑');
  await drawer.getByRole('button', { name: '更新草稿保存依据，保留当前编辑', exact: true }).click(); await expect(name).toHaveValue('后台修改之后仍保留的未提交编辑'); const saving = page.waitForResponse(response => response.request().method() === 'POST' && response.url().endsWith('/drafts'));
  await drawer.getByRole('button', { name: '保存草稿', exact: true }).click(); const response = await saving; expect(response.status()).toBe(200); const result = await response.json(); expect(result.definition.name).toBe('后台修改之后仍保留的未提交编辑');
  await writeFile(path.join(evidence, 'draft-conflict-adoption.json'), JSON.stringify({ other: JSON.parse(other.text), saved: result }, null, 2)); await capture(page, 'draft-conflict-preserved-edit');
});
