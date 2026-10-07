import type { SimulationRequest, SimulationResult } from './generated-client';
import { parseExactJSON, stringifyExactJSON } from './precision';

const object = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === 'object' && !Array.isArray(value);
export function buildSimulationRequest(pointsText: string, historySource: 'database_snapshot' | 'provided', historyText: string, parametersText: string, order: 'event_time' | 'provided'): SimulationRequest {
  const points = parseExactJSON(pointsText);
  if (!Array.isArray(points) || points.length === 0 || points.length > 1000) throw new Error('连续输入需要包含 1 至 1000 条观测');
  points.forEach((point, index) => {
    if (!object(point) || typeof point.device_id !== 'string' || !point.device_id || typeof point.key !== 'string' || !point.key || !Object.hasOwn(point, 'value')) throw new Error(`第 ${index + 1} 条观测需要设备、测点和值`);
    if (!['number', 'bigint'].includes(typeof point.observed_ms)) throw new Error(`第 ${index + 1} 条观测需要 Unix 毫秒数值 observed_ms`);
  });
  const parameters = parseExactJSON(parametersText);
  if (!object(parameters)) throw new Error('模拟参数需要 JSON 对象');
  const allowed = new Set(['clock', 'initial_state', 'asset_versions', 'timeout_ms']);
  for (const key of Object.keys(parameters)) if (!allowed.has(key)) throw new Error(`模拟参数 ${key} 应使用页面对应的输入项`);
  const request = { ...parameters, points, order } as SimulationRequest;
  if (historySource === 'provided') {
    const history = parseExactJSON(historyText);
    if (!Array.isArray(history) || history.length > 40000) throw new Error('自行提供的历史需要为数组，最多 40000 条');
    request.history = history;
  }
  // Also reject a numeric value that was already rounded before submission.
  stringifyExactJSON(request);
  return request;
}
export function simulationReplay(draftID: string, request: SimulationRequest, result: SimulationResult) {
  const points = [...(result.results ?? [])].sort((a, b) => Number(a.input_index ?? 0) - Number(b.input_index ?? 0)).map(step => step.point);
  return {
    draft_id: draftID,
    identities: { draft_revision: result.draft_revision, plan_id: result.plan_id, plan_sha256: result.plan_sha256, content_sha256: result.content_sha256, input_sha256: result.input_sha256, history_sha256: result.history_sha256 },
    history_source: result.history_source,
    request: { ...request, expected_version: result.draft_revision, points, order: result.order, clock: result.clock, timeout_ms: result.timeout_ms, history: result.history ?? [], asset_versions: result.asset_versions ?? [] },
    result,
  };
}
