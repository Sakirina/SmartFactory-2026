import type { Task } from './generated-client';
const states: Record<string, string> = { available: '等待执行', scheduled: '按计划等待执行', pending: '等待调度', running: '正在处理', retryable: '等待自动重试', completed: '已完成', cancelled: '已取消', discarded: '尝试次数已用完' };
export const taskStateText = (state: string) => states[state] ?? state;
export type TaskNotice = { kind: 'success'; title: string; taskID: string } | { kind: 'warning' | 'error'; title: string; description: string };
export function taskNoticeDescription(notice: TaskNotice, task?: Task): string | undefined {
  if (notice.kind !== 'success') return notice.description;
  return task?.id === notice.taskID ? taskPhaseText(task) : undefined;
}
export function taskPhaseText(task: Pick<Task, 'state' | 'phase' | 'business_state' | 'cancel_requested_ms' | 'progress'>): string {
  if (task.state === 'running' && task.cancel_requested_ms && task.cancel_requested_ms !== '0') return '取消请求已提交，正在停止后续处理';
  if (task.phase === 'rollups') {
    if (task.business_state === 'failed') return '汇总更新失败，等待继续处理';
    if (task.business_state === 'cancelled') return '汇总更新已停止，已提交的结果继续保存';
    return '历史计算已完成，正在更新汇总';
  }
  if (task.state === 'completed' && task.phase === 'completed') return '历史计算与汇总更新已完成';
  if (task.phase === 'replaying') return task.business_state === 'failed' ? '历史计算失败，检查点已保存' : task.business_state === 'cancelled' ? '历史计算已停止，检查点已保存' : '正在计算历史输入';
  return taskStateText(task.state);
}
