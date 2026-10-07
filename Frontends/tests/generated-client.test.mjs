import assert from 'node:assert/strict';
import { after, test } from 'node:test';
import { mkdtemp, readFile, rm, writeFile, symlink } from 'node:fs/promises';
import { fileURLToPath, pathToFileURL } from 'node:url';
import path from 'node:path';
import { tmpdir } from 'node:os';
import ts from 'typescript';

// Compile the production modules: requests must exercise the same lossless
// decoder and generated route catalogue that the browser uses.
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const temporary = await mkdtemp(path.join(tmpdir(), 'smartfactory-generated-client-test-'));
await symlink(path.join(root, 'node_modules'), path.join(temporary, 'node_modules'));
const inputs = {
  'api-types': path.join(root, '../contracts/1.0/api-types.ts'),
  api: path.join(root, 'src/transport.ts'),
  precision: path.join(root, 'src/precision.ts'),
  'generated-client': path.join(root, 'src/generated-client.ts'),
  'generated-routes': path.join(root, 'src/generated-routes.ts'),
  'simulation-input': path.join(root, 'src/simulation-input.ts'),
  'task-status': path.join(root, 'src/task-status.ts'),
  'control-status': path.join(root, 'src/control-status.ts'),
  'history-input': path.join(root, 'src/history-input.ts'),
};
for (const [name, filename] of Object.entries(inputs)) {
  const source = (await readFile(filename, 'utf8'))
    .replaceAll('../../contracts/1.0/api-types', './api-types.mjs')
    .replaceAll("'./api'", "'./api.mjs'")
    .replaceAll("'./transport'", "'./api.mjs'")
    .replaceAll("'./generated-routes'", "'./generated-routes.mjs'")
    .replaceAll("'./generated-client'", "'./generated-client.mjs'")
    .replaceAll("'./precision'", "'./precision.mjs'");
  const output = ts.transpileModule(source, {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 },
  }).outputText;
  await writeFile(path.join(temporary, name + '.mjs'), output);
}
after(() => rm(temporary, { recursive: true, force: true }));
globalThis.location = { host: 'generated-client.test' };
const values = new Map();
globalThis.sessionStorage = { getItem: key => values.get(key), setItem: (key, value) => values.set(key, value), removeItem: key => values.delete(key) };
globalThis.window = new EventTarget();
const client = await import(pathToFileURL(path.join(temporary, 'generated-client.mjs')));
const { session, RequestError } = await import(pathToFileURL(path.join(temporary, 'api.mjs')));
const precision = await import(pathToFileURL(path.join(temporary, 'precision.mjs')));
const simulation = await import(pathToFileURL(path.join(temporary, 'simulation-input.mjs')));
const taskStatus = await import(pathToFileURL(path.join(temporary, 'task-status.mjs')));
const control = await import(pathToFileURL(path.join(temporary, 'control-status.mjs')));
const history = await import(pathToFileURL(path.join(temporary, 'history-input.mjs')));
session.set('test-session');

test('generated publish keeps unsafe integers, credentials, escaped IDs and draft revision', async () => {
  globalThis.fetch = async (url, options) => {
    assert.equal(url, '/api/sf/v1/drafts/group%2Ftemperature/publish');
    assert.equal(options.method, 'POST');
    assert.equal(options.headers.Authorization, 'Bearer test-session');
    assert.deepEqual(JSON.parse(options.body), { expected_version: 5 });
    return new Response('{"id":"group/temperature","version":2,"value":9223372036854775807,"negative":-9223372036854775808,"safe":42}');
  };
  const result = await client.publishDefinitionDraft({ id: 'group/temperature', version: 5 });
  assert.equal(result.value, '9223372036854775807');
  assert.equal(result.negative, '-9223372036854775808');
  assert.equal(result.safe, 42);
});

test('conflict status and message reach the editor without a retry or publication', async () => {
  let count = 0;
  globalThis.fetch = async () => { count++; return new Response('{"error":"draft version changed"}', { status: 409 }); };
  await assert.rejects(client.publishDefinitionDraft({ id: 'draft', version: 4 }), error => error instanceof RequestError && error.status === 409 && error.message === 'draft version changed');
  assert.equal(count, 1);
});

test('generated legacy retry accepts HTTP 202 and keeps the linked task and exact business version', async () => {
  globalThis.fetch = async (url, options) => {
    assert.equal(url, '/api/sf/v1/jobs/legacy%2Fjob/retry');
    assert.equal(options.method, 'POST');
    assert.equal(options.body, undefined);
    return new Response('{"id":"legacy/job","task_id":"recompute:legacy/job:1","version":9223372036854775807}', { status: 202 });
  };
  const result = await client.callOperation('post_jobs_id_retry', { parameters: { path: { id: 'legacy/job' } } });
  assert.equal(result.task_id, 'recompute:legacy/job:1');
  assert.equal(result.version, '9223372036854775807');
});

test('expired authentication reaches the shared session event', async () => {
  let expired = 0;
  const listener = () => expired++;
  window.addEventListener('sf-auth-expired', listener);
  try {
    globalThis.fetch = async () => new Response('{"error":"authentication required"}', { status: 401 });
    await assert.rejects(client.validateDefinitionDraft({ id: 'draft', version: 4 }), error => error instanceof RequestError && error.status === 401);
    assert.equal(expired, 1);
  } finally { window.removeEventListener('sf-auth-expired', listener); }
});

test('save preserves graph values while taking authoritative persistence metadata', async () => {
  const draft = { id: 'draft', version: 3, base_version: 1, definition: { id: 'definition', nodes: [{ id: 'n', params: { value: '9223372036854775807' } }] } };
  globalThis.fetch = async (_url, options) => {
    assert.equal(JSON.parse(options.body).draft.definition.nodes[0].params.value, '9223372036854775807');
    return new Response('{"id":"draft","version":4,"base_version":1,"author_id":"engineer","updated_ms":1791122145282,"definition":{"id":"definition","nodes":[]}}');
  };
  const saved = await client.saveDefinitionDraft(draft);
  assert.equal(saved.version, 4);
  assert.equal(saved.author_id, 'engineer');
  assert.deepEqual(saved.definition.nodes, draft.definition.nodes);
});

test('continuous simulation preserves numeric identity, revision and explicit empty history', async () => {
  const request = simulation.buildSimulationRequest('[{"id":"input-b","device_id":"counter-1","key":"pulse","value":9223372036854775807,"observed_ms":1800000001000,"source_sequence":18446744073709551615}]', 'provided', '[]', '{}', 'event_time');
  globalThis.fetch = async (url, options) => {
    assert.equal(url, '/api/sf/v1/drafts/draft%2Fcounter/simulate');
    assert.equal(options.headers.Authorization, 'Bearer test-session');
    assert.match(options.body, /"value":9223372036854775807/);
    assert.match(options.body, /"source_sequence":18446744073709551615/);
    assert.equal(JSON.parse(options.body).expected_version, 8);
    assert.deepEqual(JSON.parse(options.body).history, []);
    return new Response('{"draft_revision":8,"history_source":"provided","results":[{"input_index":0,"point":{"id":"input-b","value":9223372036854775807,"source_sequence":18446744073709551615},"evaluation":{"values":{"result":9223372036854775807}}}],"final_state":{"counter-1":{"count":{"count":9223372036854775807}}}}');
  };
  const result = await client.simulateDefinitionDraft('draft/counter', 8, request);
  assert.equal(result.results[0].point.value, 9223372036854775807n);
  assert.equal(result.results[0].point.source_sequence, 18446744073709551615n);
  const exported = precision.stringifyExactJSON(simulation.simulationReplay('draft/counter', request, result), 2);
  assert.match(exported, /"value": 9223372036854775807/);
  assert.match(exported, /"count": 9223372036854775807/);
});

test('database history remains omitted and advanced fields cannot change selected inputs or revision', () => {
  const points = '[{"device_id":"counter-1","key":"pulse","value":1,"observed_ms":1800000001000}]';
  const request = simulation.buildSimulationRequest(points, 'database_snapshot', '[]', '{"timeout_ms":1000}', 'provided');
  assert.equal(Object.hasOwn(request, 'history'), false);
  assert.equal(request.order, 'provided');
  assert.throws(() => simulation.buildSimulationRequest(points, 'provided', '[]', '{"expected_version":5}', 'provided'), /expected_version/);
  assert.throws(() => simulation.buildSimulationRequest('[]', 'provided', '[]', '{}', 'provided'), /1 至 1000/);
});

test('exact JSON roundtrip distinguishes integer values from quoted identifiers and rejects unsafe Numbers', () => {
  const source = '{"integer":9007199254740993,"negative":-9223372036854775808,"unsigned":18446744073709551615,"id":"9007199254740993","nested":[{"\\u0000smartfactory-integer:":"\\u0000smartfactory-integer:0"}],"decimal":1.25}';
  const parsed = precision.parseExactJSON(source);
  assert.equal(parsed.integer, 9007199254740993n);
  assert.equal(parsed.id, '9007199254740993');
  assert.deepEqual(precision.parseExactJSON(precision.stringifyExactJSON(parsed)), parsed);
  assert.throws(() => precision.stringifyExactJSON({ value: 9007199254740992 }), /精确范围/);
  assert.throws(() => precision.parseExactJSON('{"value":0123}'), SyntaxError);
  assert.throws(() => precision.parseExactJSON('{"value":9223372036854775807e}'), SyntaxError);
});

test('task actions preserve the opaque version and escaped identity without coercion or automatic retry', async () => {
  const snapshot = { id: 'recompute:manual/group:1', version: 'opaque:0009007199254740993/authorization:3' };
  let count = 0;
  globalThis.fetch = async (url, options) => {
    count++;
    assert.equal(url, '/api/sf/v1/tasks/recompute%3Amanual%2Fgroup%3A1/retry');
    assert.equal(JSON.parse(options.body).expected_version, snapshot.version);
    return new Response('{"error":"task version changed"}', { status: 409 });
  };
  await assert.rejects(client.changeTask(snapshot, 'retry'), error => error.status === 409);
  assert.equal(count, 1);
});

test('task pagination follows the server cursor even when an authorized page is empty', async () => {
  globalThis.fetch = async url => {
    assert.equal(url, '/api/sf/v1/tasks?after=recompute%3Aa%2F1%3A1&limit=100');
    return new Response('{"items":[],"next":"recompute:b:1"}');
  };
  assert.deepEqual(await client.listTasks('recompute:a/1:1'), { items: [], next: 'recompute:b:1' });
});

test('task details keep exact queue IDs and actions returned for the current principal', async () => {
  globalThis.fetch = async () => new Response('{"id":"task:1","version":"snapshot","state":"retryable","river_id":9223372036854775807,"allowed_actions":[]}');
  const detail = await client.getTask('task:1');
  assert.equal(detail.river_id, '9223372036854775807');
  assert.deepEqual(detail.allowed_actions, []);
  globalThis.fetch = async () => new Response('{"error":"resource access denied"}', { status: 403 });
  await assert.rejects(client.getTask('task:1'), error => error.status === 403);
});

test('full point progress continues to show active rollups, failures and running cancellation', () => {
  assert.equal(taskStatus.taskStateText('pending'), '等待调度');
  assert.equal(taskStatus.taskPhaseText({ state: 'running', business_state: 'finalizing', phase: 'rollups', progress: 1 }), '历史计算已完成，正在更新汇总');
  assert.equal(taskStatus.taskPhaseText({ state: 'retryable', business_state: 'failed', phase: 'rollups', progress: 1 }), '汇总更新失败，等待继续处理');
  assert.equal(taskStatus.taskPhaseText({ state: 'running', cancel_requested_ms: 1800000001000, phase: 'rollups', progress: 1 }), '取消请求已提交，正在停止后续处理');
});

test('task operation feedback follows refreshed detail while preserving conflict explanations', () => {
  const notice = { kind: 'success', title: '任务已提交重试', taskID: 'task:1' };
  const task = { id: 'task:1', state: 'running', phase: 'rollups', business_state: 'finalizing', progress: 1 };
  assert.equal(taskStatus.taskNoticeDescription(notice, task), '历史计算已完成，正在更新汇总');
  assert.equal(taskStatus.taskNoticeDescription(notice, { ...task, state: 'completed', phase: 'completed', business_state: 'completed' }), '历史计算与汇总更新已完成');
  assert.equal(taskStatus.taskNoticeDescription(notice, { ...task, state: 'discarded', business_state: 'failed' }), '汇总更新失败，等待继续处理');
  assert.equal(taskStatus.taskNoticeDescription(notice, { ...task, state: 'cancelled', business_state: 'cancelled' }), '汇总更新已停止，已提交的结果继续保存');
  assert.equal(taskStatus.taskNoticeDescription(notice, { ...task, cancel_requested_ms: 1800000001000 }), '取消请求已提交，正在停止后续处理');
  assert.equal(taskStatus.taskNoticeDescription(notice, undefined), undefined);
  assert.equal(taskStatus.taskNoticeDescription(notice, { ...task, id: 'task:2' }), undefined);
  const conflict = { kind: 'warning', title: '任务版本或状态已经变化', description: '服务返回：task version changed' };
  assert.equal(taskStatus.taskNoticeDescription(conflict, { ...task, state: 'completed', phase: 'completed' }), conflict.description);
});


test('control requests retain exact local/source versions and asynchronous acceptance', async () => {
  const detail = { execution: { downlink_id: '执行:line/a', version: 9223372036854775807n, status: 'result_unknown' }, source_version: 9, allowed_actions: [{ action: 'reconcile', allowed: true }] };
  const body = control.executionAction(detail, '  查询原命令回执  ', 'operation:a/1');
  let requests = 0;
  globalThis.fetch = async (url, options) => {
    requests++;
    assert.equal(url, '/api/sf/v1/executions/%E6%89%A7%E8%A1%8C%3Aline%2Fa/reconcile');
    assert.match(options.body, /"expected_version":9223372036854775807/);
    assert.equal(precision.parseExactJSON(options.body).expected_source_version, 9);
    assert.equal(precision.parseExactJSON(options.body).operation_id, 'operation:a/1');
    return new Response('{"id":"operation:a/1","status":"pending","action":"reconcile","expected_source_version":9}', { status: 202 });
  };
  const result = await client.changeExecution(detail.execution.downlink_id, 'reconcile', body);
  assert.equal(requests, 1);
  assert.equal(result.status, 'pending');
  assert.equal(detail.execution.status, 'result_unknown');
  assert.equal(control.operationStatusText(result), '等待边缘处理');
  assert.throws(() => control.executionAction(detail, ' ', 'operation'), /理由/);
});

test('operation feedback uses persisted start time and current execution outcome', () => {
  assert.equal(control.operationStatusText({ status: 'pending', started_ms: 1800000000000 }), '已开始处理，等待结果');
  assert.equal(control.operationStatusText({ status: 'pending', started_ms: 0 }), '等待边缘处理');
  assert.equal(control.operationNoticeType({ status: 'pending' }), 'info');
  assert.equal(control.operationNoticeType({ status: 'pending', started_ms: 1800000000000 }), 'info');
  assert.equal(control.operationDescription({ status: 'pending', target_node_id: 'edge-a' }), '请求已保存，等待节点edge-a处理');
  assert.match(control.operationDescription({ status: 'pending', started_ms: 1800000000000, target_node_id: 'edge-a' }), /已开始处理/);
  assert.equal(control.operationNoticeType({ status: 'expired' }), 'warning');
  assert.equal(control.operationNoticeType({ status: 'rejected' }), 'warning');
  assert.equal(control.operationNoticeType({ status: 'completed' }, { execution: { status: 'completed' } }), 'success');
  assert.equal(control.operationNoticeType({ status: 'completed' }, { execution: { status: 'result_unknown' } }), 'warning');
  assert.equal(control.operationNoticeType({ status: 'completed' }, { execution: { status: 'queued' } }), 'info');
  const completed = { status: 'completed', result: { status: 'ready_to_resume' } };
  assert.equal(control.operationDescription(completed, { execution: { status: 'result_unknown' } }), '原命令结果待核对');
  assert.equal(control.operationDescription(completed, { execution: { status: 'completed' } }), '正常步骤已完成');
  assert.equal(control.operationDescription({ status: 'rejected', error: 'source version changed' }), 'source version changed');
});

test('history request preserves retained versus empty history, scope, versions and numeric observations', () => {
  const form = { id: '', kind: 'compare', definitionID: 'factory/rule', from: '1800000000000', to: '1800000002000', devices: 'counter-1, counter-1', keys: 'pulse', leftVersion: '1', rightVersion: '2', order: 'provided', inputSource: 'provided', points: '[{"id":"input:a/1","device_id":"counter-1","key":"pulse","value":9007199254740993,"observed_ms":1800000000000,"quality":"GOOD"}]', historySource: 'retained', history: '[]', parameters: '{"clock":{"freshness_ms":5000},"initial_state":{}}' };
  const retained = history.buildHistoryRequest(form);
  assert.equal(Object.hasOwn(retained, 'history'), false);
  assert.equal(retained.points[0].value, 9007199254740993n);
  assert.equal(retained.left_version, 1); assert.equal(retained.right_version, 2);
  assert.deepEqual(retained.device_ids, ['counter-1']);
  const empty = history.buildHistoryRequest({ ...form, historySource: 'provided' });
  assert.deepEqual(empty.history, []);
  assert.match(precision.stringifyExactJSON(empty), /"value":9007199254740993/);
  assert.throws(() => history.buildHistoryRequest({ ...form, from: '1800000003000' }), /结束时间/);
  assert.throws(() => history.buildHistoryRequest({ ...form, parameters: '{"kind":"replay"}' }), /kind/);
});

test('analysis pagination permits empty authorized pages and preserves precise node values', async () => {
  globalThis.fetch = async (url) => {
    if (url.startsWith('/api/sf/v1/analysis-runs?')) return new Response('{"items":[],"next":"run:a/9"}');
    assert.equal(url, '/api/sf/v1/analysis-runs/run%3Aa%2F1/steps?after=100&limit=100');
    return new Response('{"items":[{"input_index":100,"point":{"value":9007199254740993},"lanes":[{"side":"left","evaluation":{"values":{"count":9007199254740994}}}]}],"next":101}');
  };
  assert.deepEqual(await client.listAnalysisRuns('', 100), { items: [], next: 'run:a/9' });
  const page = await client.getAnalysisSteps('run:a/1', 100);
  assert.equal(page.items[0].point.value, 9007199254740993n);
  assert.equal(page.items[0].lanes[0].evaluation.values.count, 9007199254740994n);
  assert.equal(page.next, 101);
  globalThis.fetch = async () => new Response('{"items":[],"next":9223372036854775807}');
  await assert.rejects(client.getAnalysisSteps('run'), /分页游标/);
});

test('shadow stop uses the read management version without retrying a conflict', async () => {
  let requests = 0;
  globalThis.fetch = async (url, options) => {
    requests++; assert.equal(url, '/api/sf/v1/shadow-candidates/candidate%3Aa%2F1/stop');
    assert.equal(options.body, '{"expected_version":9}');
    return new Response('{"error":"candidate version changed"}', { status: 409 });
  };
  await assert.rejects(client.stopShadowCandidate({ id: 'candidate:a/1', version: 9 }), error => error instanceof RequestError && error.status === 409);
  assert.equal(requests, 1);
  const request = history.buildShadowRequest({ id: 'candidate', definitionID: 'rule', version: '2', devices: 'counter-1', keys: 'pulse', initialState: '{}' }, 9);
  assert.equal(request.expected_version, 9); assert.equal(request.version, 2);
});

test('formal source and waiting parent labels preserve independent comparison meaning', () => {
  assert.equal(history.historyStatusText('waiting_parent'), '等待前序影子计算完成');
  assert.equal(history.formalStatusText('realtime_evaluation'), '实时正式求值');
  assert.equal(history.formalStatusText('historical_recompute_projection'), '正式历史补算求值');
  assert.equal(history.formalStatusText('no_corresponding_formal_evaluation'), '暂无对应正式求值');
  assert.equal(history.historyComparisonText('replay', { lanes: [{}] }), '已返回此次回放结果');
  assert.equal(history.historyComparisonText('compare', {}), '尚未取得比较结果');
  assert.equal(history.historyComparisonText('compare', { difference: { changed: false } }), '行为一致');
  assert.equal(history.historyComparisonText('shadow', { lanes: [{}] }), '暂无对应正式求值');
  assert.equal(history.historyComparisonText('shadow', { formal_status: 'historical_recompute_projection' }), '尚未取得比较结果');
  assert.equal(history.historyComparisonText('shadow', { formal_status: 'historical_recompute_projection', formal_difference: { changed: true, values: true } }), '数值');
});
