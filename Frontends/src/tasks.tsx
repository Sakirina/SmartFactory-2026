import { useCallback, useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Descriptions, Drawer, Input, Modal, Progress, Space, Table, Tag } from 'antd';
import { RequestError } from './api';
import { changeTask, getTask, listTasks, type Task } from './generated-client';
import { exactTimestamp } from './precision';
import { taskNoticeDescription, taskPhaseText, taskStateText, type TaskNotice } from './task-status';
import { PageTitle } from './views';
const errorText = (error: unknown) => error instanceof Error ? error.message : String(error);
const taskNames: Record<string, string> = { recompute: '历史补算', archive: '历史归档', tb_entity: '设备投影', tb_definition: '定义投影', tb_alarm: '告警投影' };
function Status({ task }: { task: Task }) { return <Tag color={task.state === 'completed' ? 'success' : ['retryable', 'discarded'].includes(task.state) ? 'warning' : task.state === 'cancelled' ? 'default' : 'processing'}>{taskStateText(task.state)}</Tag>; }
function useGeneratedResource<T>(key: string | null, loader: (signal: AbortSignal) => Promise<T>, refreshMS: number) {
  const [data, setData] = useState<T>();
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const [revision, setRevision] = useState(0);
  const active = useRef<AbortController | null>(null);
  const reload = useCallback(() => { active.current?.abort(); setRevision(value => value + 1); }, []);
  useEffect(() => {
    setData(undefined); setError('');
    if (!key) return;
    let disposed = false;
    const load = async () => {
      if (active.current) return;
      const controller = new AbortController(); active.current = controller; setLoading(true);
      try { const value = await loader(controller.signal); if (!disposed && !controller.signal.aborted) { setData(value); setError(''); } }
      catch (error) { if (!disposed && !controller.signal.aborted) { setData(undefined); setError(errorText(error)); } }
      finally { if (active.current === controller) active.current = null; if (!disposed) setLoading(false); }
    };
    void load();
    const period = refreshMS === -1 ? 1000 : refreshMS;
    const timer = period > 0 ? window.setInterval(() => void load(), period) : undefined;
    return () => { disposed = true; active.current?.abort(); active.current = null; if (timer) window.clearInterval(timer); };
  }, [key, loader, refreshMS, revision]);
  return { data, error, loading, reload };
}
export function BackgroundTasks({ refreshMS, initialID = '' }: { refreshMS: number; initialID?: string }) {
  const [cursors, setCursors] = useState(['']);
  const after = cursors[cursors.length - 1];
  const loadList = useCallback((signal: AbortSignal) => listTasks(after, 100, signal), [after]);
  const list = useGeneratedResource('tasks:' + after, loadList, refreshMS);
  const [selectedID, setSelectedID] = useState(initialID);
  const [inputID, setInputID] = useState(initialID);
  useEffect(() => { setSelectedID(initialID); setInputID(initialID); }, [initialID]);
  const loadDetail = useCallback((signal: AbortSignal) => getTask(selectedID, signal), [selectedID]);
  const detail = useGeneratedResource(selectedID || null, loadDetail, refreshMS);
  const [confirmation, setConfirmation] = useState<{ task: Task; action: 'cancel' | 'retry' }>();
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<TaskNotice>();
  const select = (id: string) => { setNotice(undefined); setSelectedID(id); setInputID(id); };
  const mutate = async () => {
    if (!confirmation) return;
    setBusy(true); setNotice(undefined);
    try {
      const updated = await changeTask(confirmation.task, confirmation.action);
      setNotice({ kind: 'success', title: confirmation.action === 'retry' ? '任务已提交重试' : updated.state === 'cancelled' ? '已取消' : '已提交取消请求', taskID: updated.id });
    } catch (error) {
      setNotice(error instanceof RequestError && error.status === 409
        ? { kind: 'warning', title: '任务版本或状态已经变化', description: '详情已更新，请核对当前状态与许可操作后再次操作。服务返回：' + error.message }
        : { kind: 'error', title: error instanceof RequestError && error.status === 403 ? '当前身份无权执行此操作' : '任务操作未完成', description: errorText(error) });
    } finally { setConfirmation(undefined); setBusy(false); detail.reload(); list.reload(); }
  };
  const task = detail.data;
  const close = () => { setSelectedID(''); setConfirmation(undefined); if (initialID) location.hash = 'tasks'; };
  return <><PageTitle title="后台任务" note="查看历史补算、归档和外部投影的业务身份、执行阶段与失败原因，按照当前许可处理任务。" actions={<Button onClick={list.reload}>更新任务列表</Button>} />
    <div className="query-toolbar"><Input aria-label="任务身份" placeholder="输入完整任务身份" value={inputID} onChange={event => setInputID(event.target.value)} style={{ minWidth: 260 }} /><Button disabled={!inputID.trim()} onClick={() => select(inputID.trim())}>查看指定任务</Button></div>
    {list.error && <Alert className="error-notice" showIcon type="error" title="任务列表读取未完成" description={list.error} />}
    <Card><Table<Task> rowKey="id" dataSource={list.data?.items ?? []} loading={list.loading} pagination={false} scroll={{ x: 1100 }} columns={[
      { title: '任务 / 业务身份', render: (_, row) => <><Button type="link" onClick={() => select(row.id)}>{taskNames[row.kind ?? ''] || row.kind}</Button><small className="table-subtext">{row.business_id}<br />{row.id}</small></> },
      { title: '涉及资源', render: (_, row) => (row.resources ?? []).join('、') || '未提供资源' },
      { title: '队列状态 / 业务阶段', render: (_, row) => <><Status task={row} /><small className="table-subtext">{taskPhaseText(row)}</small></> },
      { title: '尝试次数', render: (_, row) => `${String(row.attempt ?? 0)} / ${String(row.max_attempts ?? 0)}` },
      { title: '逐点计算进度', render: (_, row) => row.kind === 'recompute' ? <Progress percent={Math.round((row.progress ?? 0) * 100)} size="small" status={row.business_state === 'failed' ? 'exception' : row.phase === 'completed' ? 'success' : 'active'} /> : '—' },
      { title: '下次执行', render: (_, row) => exactTimestamp(row.next_ms) },
      { title: '最近失败', dataIndex: 'error' },
      { title: '操作', render: (_, row) => <Button onClick={() => select(row.id)}>任务详情</Button> },
    ]} /><Space className="section-actions"><Button disabled={cursors.length <= 1} onClick={() => setCursors(values => values.slice(0, -1))}>上一页</Button><span>第 {cursors.length} 页</span><Button disabled={!list.data?.next || list.loading} onClick={() => { if (list.data?.next) setCursors(values => [...values, list.data!.next]); }}>下一页</Button></Space></Card>
    <Drawer title="后台任务详情" open={!!selectedID} onClose={close} width="min(820px, 100vw)" extra={<Button onClick={detail.reload} loading={detail.loading}>更新详情</Button>}>
      {notice && <Alert className="error-notice" showIcon type={notice.kind} title={notice.title} description={taskNoticeDescription(notice, task)} />}
      {detail.error && <Alert showIcon type="error" title="任务详情读取未完成" description={detail.error} />}
      {task && <div data-testid="task-detail"><Descriptions bordered column={1} size="small" items={[
        { key: 'id', label: '任务身份', children: task.id }, { key: 'business', label: '业务身份 / 创建版本', children: `${task.business_id ?? ''} / ${String(task.business_version ?? '')}` }, { key: 'river', label: '队列行号 / 队列', children: `${String(task.river_id)} / ${task.queue}` }, { key: 'outbox', label: '原投递记录', children: task.outbox_id || '未关联投递记录' }, { key: 'replay', label: '补算恢复身份', children: task.replay_id || '尚未建立恢复记录' }, { key: 'resources', label: '涉及资源', children: (task.resources ?? []).join('、') },
        { key: 'state', label: '队列状态', children: <Status task={task} /> }, { key: 'phase', label: '业务阶段', children: taskPhaseText(task) }, { key: 'businessState', label: '业务状态', children: task.business_state || '未提供业务状态' }, { key: 'attempts', label: '已开始次数 / 上限', children: `${String(task.attempt ?? 0)} / ${String(task.max_attempts ?? 0)}` }, { key: 'next', label: '下一次执行时间', children: exactTimestamp(task.next_ms) }, { key: 'created', label: '创建时间', children: exactTimestamp(task.created_ms) }, { key: 'started', label: '开始时间', children: exactTimestamp(task.started_ms) }, { key: 'finished', label: '结束时间', children: exactTimestamp(task.finished_ms) }, { key: 'cancel', label: '取消请求时间', children: exactTimestamp(task.cancel_requested_ms) }, { key: 'cursor', label: '历史窗口游标', children: exactTimestamp(task.cursor_ms) }, { key: 'error', label: '最近失败原因', children: task.error || '暂无失败记录' }, { key: 'replacement', label: '任务代际', children: task.superseded ? '同一业务已有更新的任务' : '当前业务任务' },
      ]} />
      {task.kind === 'recompute' && <div className="section-card"><h4>逐点计算进度</h4><Progress percent={Math.round((task.progress ?? 0) * 100)} status={task.business_state === 'failed' ? 'exception' : task.phase === 'completed' ? 'success' : 'active'} /><p>{taskPhaseText(task)}</p></div>}
      <Space className="section-actions" wrap>{task.allowed_actions?.includes('retry') && <Button type="primary" disabled={busy} onClick={() => setConfirmation({ task, action: 'retry' })}>重试任务</Button>}{task.allowed_actions?.includes('cancel') && <Button danger disabled={busy} onClick={() => setConfirmation({ task, action: 'cancel' })}>取消任务</Button>}{!task.allowed_actions?.some(action => action === 'cancel' || action === 'retry') && <p className="muted">当前身份和任务状态允许查看详情</p>}</Space>
      </div>}
    </Drawer><Modal title={confirmation?.action === 'retry' ? '重试后台任务' : '取消后台任务'} open={!!confirmation} onCancel={() => setConfirmation(undefined)} onOk={() => void mutate()} confirmLoading={busy} okText={confirmation?.action === 'retry' ? '确认重试任务' : '确认取消任务'}>
      <p>{confirmation?.action === 'retry' ? '本次重试沿用原任务与业务身份，从服务保存的进度继续处理。' : confirmation?.task.kind === 'recompute' ? '停止后续补算，保留已提交的历史结果和修订。' : '停止后续任务尝试，服务已提交的处理结果继续保存。'}</p><p>任务身份：{confirmation?.task.id}</p>
    </Modal>
  </>;
}
