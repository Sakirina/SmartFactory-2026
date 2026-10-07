import type { Contract } from './generated-client';

export const executionStates: Record<string, string> = {
  awaiting_approval: '等待会签', approved: '会签要求已满足', queued: '等待开始执行', running: '正在执行正常步骤', degraded_running: '正在执行降级步骤',
  result_unknown: '原命令结果待核对', ready_to_resume: '已核对结果，等待恢复剩余步骤', completed: '正常步骤已完成', degraded_completed: '降级步骤已完成',
  failed: '已取得明确失败结果', rejected: '条件、授权或版本未满足要求', cancelled: '后续步骤已停止', cancelled_result_unknown: '后续步骤已停止，原命令结果继续核对',
};
export const controlActionNames: Record<string, string> = { cancel: '取消后续步骤', reconcile: '核对原命令结果', resume: '恢复剩余步骤' };
export function operationStatusText(operation: Contract<'ControlOperation'>): string {
  if (operation.status === 'pending') return Number(operation.started_ms ?? 0) > 0 ? '已开始处理，等待结果' : '等待边缘处理';
  return ({ completed: '人工操作已处理', rejected: '人工操作被拒绝', expired: '边缘处理前已过期' } as Record<string, string>)[operation.status ?? ''] ?? (operation.status || '尚未取得操作结果');
}
export function operationDescription(operation: Contract<'ControlOperation'>, detail?: Contract<'ExecutionDetail'>): string {
  if (operation.status === 'pending') {
    const target = operation.target_node_id ? `节点${operation.target_node_id}` : '现场节点';
    return Number(operation.started_ms ?? 0) > 0 ? `请求已保存，${target}已开始处理，等待结果回执` : `请求已保存，等待${target}处理`;
  }
  if (operation.status !== 'completed') return operation.error || operationStatusText(operation);
  const status = detail?.execution?.status ?? operation.result?.status;
  return status ? executionStates[status] || status : '处理结果已保存，正在读取执行详情';
}
export function operationNoticeType(operation: Contract<'ControlOperation'>, detail?: Contract<'ExecutionDetail'>): 'info' | 'warning' | 'success' | 'error' {
  if (operation.status === 'pending') return 'info';
  if (operation.status === 'rejected' || operation.status === 'expired') return 'warning';
  if (operation.status !== 'completed') return 'info';
  const status = detail?.execution?.status ?? operation.result?.status;
  if (status === 'failed' || status === 'rejected') return 'error';
  if (status === 'result_unknown' || status === 'cancelled_result_unknown') return 'warning';
  return ['completed', 'degraded_completed', 'ready_to_resume', 'cancelled'].includes(status ?? '') ? 'success' : 'info';
}
export function executionAction(detail: Contract<'ExecutionDetail'>, reason: string, operationID: string): Contract<'ExecutionAction'> {
  if (!detail.execution?.downlink_id || !detail.execution.version || detail.source_version === undefined) throw new Error('执行详情缺少当前版本，请更新详情');
  if (!reason.trim()) throw new Error('请填写此次操作的理由');
  if (!operationID) throw new Error('此次操作缺少身份');
  return { expected_version: detail.execution.version, expected_source_version: detail.source_version, operation_id: operationID, reason: reason.trim() };
}
