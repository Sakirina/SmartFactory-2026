import { readFile, writeFile } from 'node:fs/promises';
import { test, expect, directory, login, get, capture, snapshot } from './phase5-support';
import { createBatch, waitDeployment } from './phase5-release-helpers';
test('the formally saved five-template evolution resumes actual publication after the screenshot storage failure', async ({ page }) => {
  test.skip(process.env.SF_PHASE5_CASES !== 'template-adoption'); test.setTimeout(90000);
  const saved = JSON.parse(await readFile(directory + '/browser-templates-resumed-first/five-template-evolved.state.json', 'utf8'));
  await login(page); const fixed = await get(page, '/releases/' + saved.release.id); expect(fixed.sha256).toBe(saved.release.sha256);
  const id = 'frontend-five-adoption-r02-' + Date.now();
  await createBatch(page, id, fixed.id, [['release-a']], '继续原正式五模板固定发布，核对三协议实际消费者采用');
  const completed = await waitDeployment(page, id, value => value.state === 'completed' && value.targets[0].state === 'running' && value.targets[0].report_fresh, 65000);
  expect(completed.targets[0].runtime.program_sha256).toBe('b02566d928858835502d2d0cd6aac04ef561335e0498f7675fb1d60b0e71f229');
  expect(completed.targets[0].runtime.components).toHaveLength(29);
  for (const item of completed.targets[0].runtime.components) {
    const component = fixed.manifest.components.find((value: any) => value.id === item.id);
    expect(item.version).toBe(component.version); expect(item.sha256).toBe(component.sha256);
    if (item.kind === 'rule') { expect(item.applied_version).toBeGreaterThan(0); expect(item.applied_sha256).toMatch(/^[a-f0-9]{64}$/); }
    else expect(item.applied_sha256).toBe(item.sha256);
  }
  const token = (await readFile('/private/tmp/smartfactory-phase5-release-process-tenth-20261005/private/agent-a/runtime-token', 'utf8')).trim();
  const runtime = await (await fetch('http://127.0.0.1:60050/internal/releases/runtime', { headers: { Authorization: 'Bearer ' + token } })).json();
  const consumer = await (await fetch('http://127.0.0.1:60057/runtime')).json();
  const actual = consumer.connectors.filter((item: any) => item.connector_id.startsWith('frontend-five-'));
  expect(actual.map((item: any) => item.protocol).sort()).toEqual(['modbus_tcp', 'mqtt_device', 'opcua']);
  for (const item of actual) {
    const adopted = runtime.configurations.find((value: any) => value.reference.id.endsWith('/' + item.connector_id));
    expect(adopted.reference.version).toBe(1); expect(item.state.found).toBe(true);
    expect(item.state.applied_entity_revision).toBe(adopted.apply_generation); expect(item.state.public_configuration_sha256).toBe(adopted.consumer_digest);
  }
  await snapshot('five-template-real-node-running', { fixed, completed, runtime, consumer }); await capture(page, 'five-template-real-node-running');
  await writeFile(directory + '/template-final-evolution.state.json', JSON.stringify(saved, null, 2));
});
