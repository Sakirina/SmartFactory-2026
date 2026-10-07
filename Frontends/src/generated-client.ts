import type { components, operations } from '../../contracts/1.0/api-types';
import { operationRoutes } from './generated-routes';
import { api, RequestError, session } from './transport';
import { parseAPIJSON, parseExactJSON, stringifyExactJSON } from './precision';
import type { Draft } from './types';

export type Contract<T extends keyof components['schemas']> = components['schemas'][T];
export type Task = Contract<'Task'> & Required<Pick<Contract<'Task'>, 'id' | 'version' | 'state' | 'allowed_actions'>>;
export type SimulationResult = Contract<'SimulationResult'>;
export type SimulationRequest = Contract<'SimulationRequest'>;
type JsonContent<T> = T extends { content: { 'application/json': infer C } } ? C : never;
type OperationID = keyof operations & keyof typeof operationRoutes;
type OperationBody<K extends OperationID> = JsonContent<NonNullable<operations[K] extends { requestBody?: infer B } ? B : never>>;
type OperationParameters<K extends OperationID> = operations[K]['parameters'];
type OperationInput<K extends OperationID> =
  (OperationParameters<K> extends { path: object } ? { parameters: OperationParameters<K> } : { parameters?: OperationParameters<K> }) &
  (operations[K] extends { requestBody: unknown } ? { body: OperationBody<K> } : { body?: OperationBody<K> });
type SuccessResponse<T> = { [S in keyof T]: S extends 200 | 201 | 202 | 204 ? JsonContent<T[S]> : never }[keyof T];
type OperationResponse<K extends OperationID> = SuccessResponse<operations[K]['responses']>;

export async function callOperation<K extends OperationID>(operation: K, input: OperationInput<K>, options: { signal?: AbortSignal; exact?: boolean } = {}): Promise<OperationResponse<K>> {
  const route = operationRoutes[operation];
  let path: string = route.path;
  const routing = input as { parameters?: { path?: Record<string, unknown>; query?: Record<string, unknown> }; body?: unknown };
  for (const [key, value] of Object.entries(routing.parameters?.path ?? {})) path = path.replace(`{${key}}`, encodeURIComponent(String(value)));
  if (/\{[^}]+\}/.test(path)) throw new Error('请求缺少路径身份');
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(routing.parameters?.query ?? {})) if (value !== undefined && value !== null) query.set(key, String(value));
  if (query.size) path += '?' + query;
  return api<OperationResponse<K>>(path, {
    method: route.method, signal: options.signal,
    ...(routing.body === undefined ? {} : { body: stringifyExactJSON(routing.body) }),
  }, options.exact ? parseExactJSON : parseAPIJSON);
}

export const callDefinitionOperation = callOperation;
export async function readGeneratedResource<T>(resource: string, signal?: AbortSignal): Promise<T> {
  const [pathname, search = ''] = resource.split('?');
  for (const [operation, route] of Object.entries(operationRoutes)) {
    if (route.method !== 'GET') continue;
    const names: string[] = [];
    const pattern = route.path.replace(/\{([^}]+)\}/g, (_match, name: string) => { names.push(name); return '([^/]+)'; });
    const match = pathname.match(new RegExp('^' + pattern + '$'));
    if (!match) continue;
    const path = Object.fromEntries(names.map((name, index) => [name, decodeURIComponent(match[index + 1])]));
    const query = Object.fromEntries(new URLSearchParams(search));
    return await callOperation(operation as OperationID, { parameters: { path, query } } as never, { exact: true, signal }) as T;
  }
  throw new Error('统一契约未登记此读取操作：' + pathname);
}
export async function saveDefinitionDraft(draft: Draft): Promise<Draft> {
  const saved = await callOperation('post_drafts', { body: { draft, expected_version: draft.version } });
  if (typeof saved.id !== 'string' || typeof saved.version !== 'number' || typeof saved.base_version !== 'number' || typeof saved.author_id !== 'string' || typeof saved.updated_ms !== 'number' || saved.definition?.id !== draft.definition.id) {
    throw new RequestError(502, '草稿返回内容缺少版本或标识');
  }
  return { ...draft, id: saved.id, version: saved.version, base_version: saved.base_version, author_id: saved.author_id, updated_ms: saved.updated_ms, definition: { ...draft.definition, status: 'draft' } };
}
export async function validateDefinitionDraft(draft: Pick<Draft, 'id' | 'version'>): Promise<{ valid: boolean; errors: string[] }> {
  const result = await callOperation('post_drafts_id_validate', { parameters: { path: { id: draft.id } }, body: { expected_version: draft.version } });
  if (typeof result.valid !== 'boolean') throw new RequestError(502, '校验返回内容缺少结果');
  return { valid: result.valid, errors: result.errors ?? [] };
}
export function publishDefinitionDraft(draft: Pick<Draft, 'id' | 'version'>) {
  return callOperation('post_drafts_id_publish', { parameters: { path: { id: draft.id } }, body: { expected_version: draft.version } });
}
export async function simulateDefinitionDraft(id: string, revision: number, request: SimulationRequest, signal?: AbortSignal): Promise<SimulationResult> {
  const result = await callOperation('post_drafts_id_simulate', { parameters: { path: { id } }, body: { ...request, expected_version: revision } }, { exact: true, signal });
  if (!result || !('results' in result)) throw new RequestError(502, '连续模拟返回内容缺少序列结果');
  return result;
}
function taskDetail(result: Contract<'Task'>): Task {
  if (typeof result.id !== 'string' || typeof result.version !== 'string' || !result.version || typeof result.state !== 'string' || !Array.isArray(result.allowed_actions)) {
    throw new RequestError(502, '任务返回内容缺少身份、版本或许可操作');
  }
  return result as Task;
}
export async function listTasks(after = '', limit = 100, signal?: AbortSignal) {
  const result = await callOperation('get_tasks', { parameters: { query: { after, limit } } }, { signal });
  return { items: (result.items ?? []).map(taskDetail), next: result.next ?? '' };
}
export async function getTask(id: string, signal?: AbortSignal) {
  return taskDetail(await callOperation('get_tasks_id', { parameters: { path: { id } } }, { signal }));
}
export async function changeTask(task: Pick<Task, 'id' | 'version'>, action: 'cancel' | 'retry') {
  if (!task.version) throw new Error('任务详情缺少版本，请更新详情');
  return taskDetail(await callOperation(action === 'cancel' ? 'post_tasks_id_cancel' : 'post_tasks_id_retry', { parameters: { path: { id: task.id } }, body: { expected_version: task.version } }));
}

export type ExecutionRecord = Contract<'Execution'> & Required<Pick<Contract<'Execution'>, 'downlink_id' | 'version' | 'status'>>;
export type ExecutionDetail = Contract<'ExecutionDetail'> & { execution: ExecutionRecord; source_version: NonNullable<Contract<'ExecutionDetail'>['source_version']> };
export type ControlOperation = Contract<'ControlOperation'> & Required<Pick<Contract<'ControlOperation'>, 'id' | 'status'>>;
export type HistoryRun = Contract<'HistoryRun'> & Required<Pick<Contract<'HistoryRun'>, 'id' | 'task_id' | 'status'>>;
export type HistorySnapshot = Contract<'HistorySnapshot'>;
export type HistoryStep = Contract<'HistoryStep'>;
export type ShadowCandidate = Contract<'ShadowCandidate'> & Required<Pick<Contract<'ShadowCandidate'>, 'id' | 'version' | 'status'>>;
function executionRecord(value: Contract<'Execution'>): ExecutionRecord {
  if (!value.downlink_id || value.version === undefined || !value.status) throw new RequestError(502, '执行记录缺少身份、版本或状态');
  return value as ExecutionRecord;
}
export async function listExecutions(signal?: AbortSignal) {
  return (await callOperation('get_executions', {}, { exact: true, signal }) ?? []).map(executionRecord);
}
export async function getExecutionDetail(id: string, signal?: AbortSignal): Promise<ExecutionDetail> {
  const result = await callOperation('get_executions_id', { parameters: { path: { id } } }, { exact: true, signal });
  if (!result.execution || result.source_version === undefined || !Array.isArray(result.allowed_actions)) throw new RequestError(502, '执行详情缺少版本或许可操作');
  return { ...result, execution: executionRecord(result.execution) } as ExecutionDetail;
}
export async function getPublishedDefinition(id: string, version: Contract<'Definition'>['version'], signal?: AbortSignal) {
  const versions = await callOperation('get_definitions_id_versions', { parameters: { path: { id } } }, { exact: true, signal });
  return (versions ?? []).find(value => String(value.version) === String(version));
}
function controlOperation(value: Contract<'ControlOperation'>): ControlOperation {
  if (!value.id || !value.status) throw new RequestError(502, '人工操作返回内容缺少身份或状态');
  return value as ControlOperation;
}
export async function getControlOperation(id: string, signal?: AbortSignal) {
  return controlOperation(await callOperation('get_control_operations_id', { parameters: { path: { id } } }, { exact: true, signal }));
}
export async function changeExecution(id: string, action: 'cancel' | 'reconcile' | 'resume', body: Contract<'ExecutionAction'>) {
  const operation = action === 'cancel' ? 'post_executions_id_cancel' : action === 'reconcile' ? 'post_executions_id_reconcile' : 'post_executions_id_resume';
  return controlOperation(await callOperation(operation, { parameters: { path: { id } }, body }, { exact: true }));
}
function historyRun(value: Contract<'HistoryRun'>): HistoryRun {
  if (!value.id || !value.task_id || !value.status) throw new RequestError(502, '分析运行缺少身份、任务或状态');
  return value as HistoryRun;
}
export async function listAnalysisRuns(after = '', limit = 100, signal?: AbortSignal) {
  const result = await callOperation('get_analysis_runs', { parameters: { query: { after, limit } } }, { exact: true, signal });
  return { items: (result.items ?? []).map(historyRun), next: result.next ?? '' };
}
export async function getAnalysisRun(id: string, signal?: AbortSignal) {
  return historyRun(await callOperation('get_analysis_runs_id', { parameters: { path: { id } } }, { exact: true, signal }));
}
export async function createAnalysisRun(body: Contract<'HistoryRequest'>) {
  return historyRun(await callOperation('post_analysis_runs', { body }, { exact: true }));
}
export function getAnalysisSnapshot(id: string, signal?: AbortSignal) {
  return callOperation('get_analysis_runs_id_snapshot', { parameters: { path: { id } } }, { exact: true, signal });
}
export async function getAnalysisSteps(id: string, after = 0, limit = 100, signal?: AbortSignal) {
  const result = await callOperation('get_analysis_runs_id_steps', { parameters: { path: { id }, query: { after, limit } } }, { exact: true, signal });
  const next = Number(result.next ?? -1);
  if (!Number.isSafeInteger(next) || next < -1 || next > 10000) throw new RequestError(502, '结果分页游标超出分析输入范围');
  return { items: result.items ?? [], next };
}
function shadowCandidate(value: Contract<'ShadowCandidate'>): ShadowCandidate {
  if (!value.id || value.version === undefined || !value.status) throw new RequestError(502, '候选详情缺少身份、版本或状态');
  return value as ShadowCandidate;
}
export async function listShadowCandidates(signal?: AbortSignal) {
  const result = await callOperation('get_shadow_candidates', {}, { exact: true, signal });
  return (result.items ?? []).map(shadowCandidate);
}
export async function getShadowCandidate(id: string, signal?: AbortSignal) {
  return shadowCandidate(await callOperation('get_shadow_candidates_id', { parameters: { path: { id } } }, { exact: true, signal }));
}
export async function saveShadowCandidate(body: Contract<'ShadowRequest'>) {
  return shadowCandidate(await callOperation('post_shadow_candidates', { body }, { exact: true }));
}
export async function stopShadowCandidate(candidate: Pick<ShadowCandidate, 'id' | 'version'>) {
  return shadowCandidate(await callOperation('post_shadow_candidates_id_stop', { parameters: { path: { id: candidate.id } }, body: { expected_version: candidate.version } }, { exact: true }));
}

export async function streamQuery(kind: string, body: Contract<'QueryRequest'>, cursor: string, signal: AbortSignal) {
  const route = operationRoutes.post_queries_kind_events;
  return openEventStream(route.path.replace('{kind}', encodeURIComponent(kind)), body, signal, cursor ? { 'Last-Event-ID': cursor } : {});
}
export async function streamAssistant(messages: Contract<'AssistantRequest'>['messages'], signal: AbortSignal) {
  return openEventStream(operationRoutes.post_assistant.path, { messages }, signal);
}
async function openEventStream(path: string, body: unknown, signal: AbortSignal, headers: Record<string,string> = {}) {
  const response = await fetch('/api/sf/v1' + path, { method: 'POST', headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + session.get(), ...headers }, body: stringifyExactJSON(body), signal });
  if (!response.ok) {
    const value = parseAPIJSON(await response.text()) as { error?: string; detail?: string };
    if (response.status === 401) window.dispatchEvent(new Event('sf-auth-expired'));
    if (response.status === 403) window.dispatchEvent(new Event('sf-authz-changed'));
    throw new RequestError(response.status, value.error || value.detail || `流式请求失败 (${response.status})`);
  }
  if (!response.body) throw new Error('响应缺少数据流');
  return response.body;
}
