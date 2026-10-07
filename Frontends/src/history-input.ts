import type { Contract } from './generated-client';
import { parseExactJSON, stringifyExactJSON } from './precision';

const object = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === 'object' && !Array.isArray(value);
export function exactInteger(text: string, name: string, positive = true): number | bigint {
  if (!/^(?:0|[1-9][0-9]*)$/.test(text.trim())) throw new Error(`${name}需要整数`);
  const value = parseExactJSON(text.trim());
  if ((typeof value !== 'number' && typeof value !== 'bigint') || (positive ? value <= 0 : value < 0)) throw new Error(`${name}超出允许范围`);
  return value;
}
const names = (text: string) => [...new Set(text.split(/[,，\n]/).map(item => item.trim()).filter(Boolean))];
function parameters(text: string): Record<string, unknown> {
  const value = parseExactJSON(text);
  if (!object(value)) throw new Error('计算条件需要JSON对象');
  for (const key of Object.keys(value)) if (!['clock', 'initial_state', 'asset_versions'].includes(key)) throw new Error(`计算条件${key}请使用对应输入项`);
  if (value.clock !== undefined && !object(value.clock)) throw new Error('时钟需要JSON对象');
  if (value.initial_state !== undefined && !object(value.initial_state)) throw new Error('初始状态需要JSON对象');
  return value;
}
export interface HistoryFormInput { id: string; kind: 'replay' | 'compare'; definitionID: string; from: string; to: string; devices: string; keys: string; leftVersion: string; rightVersion: string; order: 'event_time' | 'provided'; inputSource: 'retained' | 'provided'; points: string; historySource: 'retained' | 'provided'; history: string; parameters: string }
export function buildHistoryRequest(input: HistoryFormInput): Contract<'HistoryRequest'> {
  if (!input.definitionID.trim()) throw new Error('请选择或填写已发布规则');
  const from = exactInteger(input.from, '开始时间'), to = exactInteger(input.to, '结束时间');
  if (BigInt(from) > BigInt(to)) throw new Error('结束时间需要晚于或等于开始时间');
  const request = { ...parameters(input.parameters), ...(input.id.trim() ? { id: input.id.trim() } : {}), kind: input.kind, definition_id: input.definitionID.trim(), from_ms: from, to_ms: to, device_ids: names(input.devices), keys: names(input.keys), order: input.order } as Contract<'HistoryRequest'>;
  if (input.kind === 'compare') { request.left_version = exactInteger(input.leftVersion, '左侧规则版本'); request.right_version = exactInteger(input.rightVersion, '右侧规则版本'); }
  if (input.inputSource === 'provided') {
    const points = parseExactJSON(input.points);
    if (!Array.isArray(points) || !points.length || points.length > 10000) throw new Error('自行提供的输入需要1至10000条观测');
    request.points = points;
  }
  if (input.historySource === 'provided') {
    const history = parseExactJSON(input.history);
    if (!Array.isArray(history) || history.length > 40000) throw new Error('自行提供的历史需要数组，最多40000条');
    request.history = history;
  }
  stringifyExactJSON(request);
  return request;
}
export function buildShadowRequest(input: { id: string; definitionID: string; version: string; devices: string; keys: string; initialState: string }, expectedVersion: Contract<'ShadowCandidate'>['version'] = 0): Contract<'ShadowRequest'> {
  if (!input.id.trim() || !input.definitionID.trim()) throw new Error('请填写候选身份和已发布规则');
  const state = parseExactJSON(input.initialState);
  if (!object(state)) throw new Error('初始状态需要JSON对象');
  return { id: input.id.trim(), definition_id: input.definitionID.trim(), version: exactInteger(input.version, '候选规则版本'), expected_version: expectedVersion, device_ids: names(input.devices), keys: names(input.keys), initial_state: state as Contract<'ShadowRequest'>['initial_state'] };
}
export function historyStatusText(status?: string): string {
  return ({ pending: '等待计算', waiting_parent: '等待前序影子计算完成', running: '正在计算', failed: '计算失败', cancelled: '已取消后续计算', completed: '计算已完成', active: '正在采集候选输入', stopped: '候选已停止采集', budget_exceeded: '采集预算已用完' } as Record<string, string>)[status ?? ''] || status || '尚未取得状态';
}
export function formalStatusText(status?: string): string {
  return ({ realtime_evaluation: '实时正式求值', historical_recompute_projection: '正式历史补算求值', no_corresponding_formal_evaluation: '暂无对应正式求值' } as Record<string, string>)[status ?? ''] || '暂无对应正式求值';
}
export function differenceText(value?: Contract<'HistoryDifference'>): string {
  if (typeof value?.changed !== 'boolean') return '尚未取得比较结果';
  if (!value.changed) return '行为一致';
  const fields: Record<string, string> = { values: '数值', quality: '质量', alarm: '告警', state: '状态', path: '节点路径', trigger: '触发判断' };
  return Object.entries(fields).filter(([key]) => value[key as keyof typeof value]).map(([, name]) => name).join('、') || '存在差异';
}
export function historyComparisonText(kind: string | undefined, step: Contract<'HistoryStep'>): string {
  if (kind === 'compare') return differenceText(step.difference);
  if (kind === 'shadow') {
    if (!['realtime_evaluation', 'historical_recompute_projection'].includes(step.formal_status ?? '')) return '暂无对应正式求值';
    return differenceText(step.formal_difference);
  }
  return step.lanes?.length ? '已返回此次回放结果' : '尚未取得回放结果';
}
