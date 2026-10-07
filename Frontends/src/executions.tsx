import { useCallback, useEffect, useState } from 'react';
import { Alert, Button, Card, Collapse, Descriptions, Drawer, Form, Input, Modal, Select, Space, Switch, Table, Tabs, Tag, Timeline } from 'antd';
import { api, json, RequestError } from './api';
import { callOperation, changeExecution, getControlOperation, getExecutionDetail, getPublishedDefinition, readGeneratedResource, type Contract, type ExecutionRecord } from './generated-client';
import { useGeneratedResource } from './generated-resource';
import { useWindowQuery } from './query';
import { controlActionNames, executionAction, executionStates, operationDescription, operationNoticeType, operationStatusText } from './control-status';
import { exactTimestamp, parseExactJSON } from './precision';
import type { Definition, User } from './types';
import { PageTitle, StateTag } from './views';

const errorText = (error: unknown) => error instanceof Error ? error.message : String(error);
const roleNames: Record<string, string> = { engineer: '工程师确认', leader: '领导确认', safety: '现场安全确认' };
const eventNames: Record<string, string> = { transition: '状态变化', approval: '人员会签', action: '设备动作', feedback: '设备反馈', evidence: '命令结果证据', operation_request: '人工操作请求', operation_started: '开始处理人工操作', operation_result: '人工操作结果' };
function ExecutionStatus({ status }: { status?: string }) { return <Tag color={['failed', 'rejected'].includes(status ?? '') ? 'error' : ['result_unknown', 'cancelled_result_unknown', 'ready_to_resume'].includes(status ?? '') ? 'warning' : ['completed', 'degraded_completed'].includes(status ?? '') ? 'success' : 'default'}>{executionStates[status ?? ''] || status || '尚未取得执行状态'}</Tag>; }
type Notice = { kind: 'success'; operationID?: string; executionID: string; title: string } | { kind: 'error' | 'warning'; title: string; description: string };

export function Executions({ user, refreshMS, local, initialID = '' }: { user: User; refreshMS: number; local: boolean; initialID?: string }) {
  const [search,setSearch] = useState(''); const [listStatus,setListStatus] = useState(''); const [pageToken,setPageToken] = useState(''); const [pages,setPages] = useState<string[]>([]);
  const queryWindow = useWindowQuery('executions',{search:search||undefined,status:listStatus||undefined,limit:20,page_token:pageToken||undefined},pageToken?0:refreshMS);
  useEffect(()=>{if(queryWindow.resetRequired && pageToken){setPageToken('');setPages([]);}},[queryWindow.resetRequired,pageToken]);
  const list = {...queryWindow,data:queryWindow.data?.items?.map(row=>row.data) as ExecutionRecord[]|undefined};
  const definitions = useGeneratedResource('execution-definitions', useCallback((signal: AbortSignal) => readGeneratedResource<Definition[]>('/definitions', signal), []));
  const [selectedID, setSelectedID] = useState(initialID);
  const [inputID, setInputID] = useState(initialID);
  useEffect(() => { setSelectedID(initialID); setInputID(initialID); }, [initialID]);
  const detail = useGeneratedResource(selectedID || null, useCallback((signal: AbortSignal) => getExecutionDetail(selectedID, signal), [selectedID]), refreshMS);
  const [operationID, setOperationID] = useState('');
  const operation = useGeneratedResource(selectedID && operationID && detail.data ? operationID : null, useCallback((signal: AbortSignal) => getControlOperation(operationID, signal), [operationID]), refreshMS);
  const [notice, setNotice] = useState<Notice>();
  const [reason, setReason] = useState('');
  const [action, setAction] = useState<'cancel' | 'reconcile' | 'resume'>();
  const [busy, setBusy] = useState(false);
  const [creating, setCreating] = useState(false);
  const [definitionID, setDefinitionID] = useState('');
  const [params, setParams] = useState('{}');
  const [override, setOverride] = useState(false);
  const [creationError, setCreationError] = useState('');
  const [lastRequest, setLastRequest] = useState<{ id: string; action: 'cancel' | 'reconcile' | 'resume'; body: Contract<'ExecutionAction'> }>();
  const execution = detail.data?.execution;
  const ruleID = execution?.definition_id ?? '';
  const ruleVersion = execution?.definition_version;
  const rule = useGeneratedResource(ruleID && execution ? `${ruleID}:${String(ruleVersion)}` : null, useCallback((signal: AbortSignal) => getPublishedDefinition(ruleID, ruleVersion, signal), [ruleID, ruleVersion]));
  const activeOperation = operation.data ?? detail.data?.operations?.find(value => value.id === operationID);
  useEffect(() => { setNotice(value => value?.kind === 'success' ? undefined : value); setAction(undefined); }, [user.id, user.version, user.roles.join('|'), user.resources.join('|')]);
  const manage = !user.ai && user.roles.some(role => ['admin', 'engineer'].includes(role));
  const select = (id: string) => { setSelectedID(id); setInputID(id); setNotice(undefined); setOperationID(''); setLastRequest(undefined); setReason(''); };
  const refresh = () => { detail.reload(); list.reload(); operation.reload(); };
  const failure = (error: unknown, title: string) => {
    setNotice(error instanceof RequestError && error.status === 409
      ? { kind: 'warning', title: '执行版本或状态已经变化', description: '详情已更新，请核对当前版本与许可操作。服务返回：' + error.message }
      : { kind: 'error', title: error instanceof RequestError && error.status === 403 ? '当前身份无权执行此操作' : title, description: errorText(error) });
  };
  const intervene = async (repeat = false) => {
    if (!detail.data || (!action && !repeat)) return;
    setBusy(true); setNotice(undefined);
    try {
      const request = repeat ? lastRequest : { id: selectedID, action: action!, body: executionAction(detail.data, reason, crypto.randomUUID()) };
      if (!request) return;
      setLastRequest(request);
      const result = await changeExecution(request.id, request.action, request.body);
      setOperationID(result.id);
      setNotice({ kind: 'success', title: controlActionNames[request.action] + '请求已受理', executionID: selectedID, operationID: result.id });
    } catch (error) { failure(error, '人工操作未完成'); }
    finally { setBusy(false); setAction(undefined); refresh(); }
  };
  const approve = async (role: string) => {
    if (!execution) return;
    setBusy(true); setNotice(undefined);
    try { await callOperation('post_executions_id_approve', { parameters: { path: { id: selectedID } }, body: { role: role as 'engineer' | 'leader' | 'safety', expected_version: execution.version } }, { exact: true }); setNotice({ kind: 'success', title: '人员会签已保存', executionID: selectedID }); }
    catch (error) { failure(error, '人员会签未完成'); }
    finally { setBusy(false); refresh(); }
  };
  const dispatch = async () => {
    if (!execution) return;
    setBusy(true); setNotice(undefined);
    try { await callOperation('post_executions_id_dispatch', { parameters: { path: { id: selectedID } }, body: { expected_version: execution.version } }, { exact: true }); setNotice({ kind: 'success', title: '正式下发请求已提交', executionID: selectedID }); }
    catch (error) { failure(error, '正式下发未完成'); }
    finally { setBusy(false); refresh(); }
  };
  const create = async () => {
    setBusy(true); setCreationError('');
    try {
      const values = parseExactJSON(params);
      if (!values || typeof values !== 'object' || Array.isArray(values)) throw new Error('预案参数需要JSON对象');
      const result = await callOperation('post_executions', { body: { definition_id: definitionID, params: values as Contract<'CreateExecutionBody'>['params'], override } }, { exact: true });
      if (!result.downlink_id) throw new Error('创建结果缺少控制身份');
      setCreating(false); select(result.downlink_id); list.reload();
    } catch (error) { setCreationError(errorText(error)); definitions.reload(); list.reload(); }
    finally { setBusy(false); }
  };
  const allowed = detail.data?.allowed_actions ?? [];
  const successVisible = notice?.kind === 'success' && execution && notice.executionID === selectedID && (!notice.operationID || (activeOperation && !operation.error));
  return <><PageTitle title="审批与执行" note="核对会签、现场条件、设备动作和反馈，按照当前许可取消、核对或恢复剩余步骤。" actions={<Space><Button onClick={list.reload}>更新执行列表</Button>{manage && <Button type="primary" onClick={() => { setCreationError(''); setCreating(true); }}>发起即时控制</Button>}</Space>} />
    <div className="query-toolbar"><Input aria-label="执行列表搜索" placeholder="搜索执行身份或预案" value={search} onChange={event=>{setSearch(event.target.value);setPageToken('');setPages([]);}} /><Select aria-label="执行列表状态" allowClear placeholder="所有执行状态" value={listStatus||undefined} onChange={value=>{setListStatus(value||'');setPageToken('');setPages([]);}} options={Object.entries(executionStates).map(([value,label])=>({value,label}))} /><Input aria-label="控制执行身份" placeholder="输入完整控制执行身份" value={inputID} onChange={event => setInputID(event.target.value)} /><Button disabled={!inputID.trim()} onClick={() => select(inputID.trim())}>查看指定执行</Button></div>
    {list.error && <Alert className="error-notice" showIcon type="error" title="执行列表读取未完成" description={list.error} />}
    <Card><Table<ExecutionRecord> rowKey="downlink_id" dataSource={list.data ?? []} pagination={false} loading={list.loading} scroll={{ x: 1050 }} columns={[
      { title: '执行 / 预案版本', render: (_, row) => <><Button type="link" onClick={() => select(row.downlink_id)}>{row.downlink_id}</Button><small className="table-subtext">{row.definition_id} · v{String(row.definition_version ?? '')}</small></> },
      { title: '执行状态', render: (_, row) => <ExecutionStatus status={row.status} /> }, { title: '风险依据', render: (_, row) => `${row.risk_category || '未指定'} / ${String(row.risk_level ?? 0)}级` },
      { title: '已有会签', render: (_, row) => `${row.approvals?.length ?? 0}人` }, { title: '请求时间', render: (_, row) => exactTimestamp(row.created_ms) }, { title: '操作', render: (_, row) => <Button onClick={() => select(row.downlink_id)}>执行详情</Button> },
    ]} /><Space className="section-actions"><Button disabled={!pages.length} onClick={()=>{setPageToken(pages.at(-1)||'');setPages(pages.slice(0,-1));}}>上一页执行</Button><Button disabled={!queryWindow.data?.has_more || !queryWindow.data.next_page_token} onClick={()=>{setPages([...pages,pageToken]);setPageToken(queryWindow.data!.next_page_token!);}}>下一页执行</Button><span>提交快照分页</span></Space></Card>
    <Modal title="发起即时控制" open={creating && manage} onCancel={() => setCreating(false)} onOk={() => void create()} confirmLoading={busy} okButtonProps={{ disabled: !definitionID }} okText="建立会签请求">
      {creationError && <Alert className="error-notice" type="error" showIcon title="控制请求未创建" description={creationError} />}
      <Form layout="vertical"><Form.Item label="已发布预案" required><Select aria-label="控制预案" value={definitionID || undefined} onChange={setDefinitionID} options={(definitions.data ?? []).filter(value => value.kind === 'strategy' && value.status === 'published').map(value => ({ value: value.id, label: `${value.name} · v${value.version}` }))} /></Form.Item><Form.Item label="预案参数"><Input.TextArea aria-label="预案参数" rows={4} value={params} onChange={event => setParams(event.target.value)} /></Form.Item><Form.Item label="请求强制执行"><Switch checked={override} onChange={setOverride} /></Form.Item><Alert showIcon type={override ? 'warning' : 'info'} title={override ? '业务类强制执行需要两名工程师与一名领导，安全类还需要指定现场安全员确认。' : '普通即时控制需要一名工程师与一名领导分别确认。'} description="正式下发后按现场条件与有效期限执行。" /></Form>
    </Modal>
    <Drawer title="控制执行详情" open={!!selectedID} width="min(1100px, 100vw)" extra={<Button loading={detail.loading} onClick={refresh}>更新执行详情</Button>} onClose={() => { select(''); setAction(undefined); if (initialID) location.hash = 'executions'; }}>
      {notice?.kind !== 'success' && notice && <Alert className="error-notice" showIcon type={notice.kind} title={notice.title} description={notice.description} />}
      {successVisible && notice?.kind === 'success' && <Alert className="error-notice" data-testid="execution-operation-notice" showIcon type={activeOperation ? operationNoticeType(activeOperation, detail.data) : notice.title === '正式下发请求已提交' ? 'info' : 'success'} title={notice.operationID && activeOperation ? operationStatusText(activeOperation) : notice.title} description={notice.operationID && activeOperation ? operationDescription(activeOperation, detail.data) : executionStates[execution!.status] || execution!.status} />}
      {detail.error && <Alert showIcon type="error" title="执行详情读取未完成" description={detail.error} />}
      {operation.error && detail.data && <Alert className="error-notice" showIcon type="error" title="人工操作结果读取未完成" description={operation.error} />}
      {rule.error && detail.data && <Alert className="error-notice" showIcon type="error" title="采用预案读取未完成" description={rule.error} />}
      {execution && detail.data && <div data-testid="execution-detail"><Descriptions bordered size="small" column={{ xs: 1, sm: 2 }} items={[
        { key: 'id', label: '执行身份', children: execution.downlink_id }, { key: 'rule', label: '预案版本', children: `${execution.definition_id} · v${String(execution.definition_version)}` },
        { key: 'state', label: '当前执行状态', children: <ExecutionStatus status={execution.status} /> }, { key: 'reason', label: '当前处理依据', children: execution.reason || '暂无补充说明' },
        { key: 'version', label: '当前应用版本', children: String(execution.version) }, { key: 'sourceVersion', label: '来源节点 / 边缘版本', children: `${detail.data.source_node_id || '尚未取得来源节点'} / ${String(detail.data.source_version)}` },
        { key: 'branch', label: '执行分支', children: execution.branch === 'degraded' ? '已配置的降级步骤' : '正常步骤' }, { key: 'authority', label: '执行协调身份 / 执行权', children: `${execution.coordinator_id || '暂无协调身份'} / ${String(execution.fence ?? '尚未取得')}` },
        { key: 'command', label: '当前原命令身份', children: execution.active_command_id || '暂无等待中的命令' }, { key: 'expires', label: '会签有效期限', children: exactTimestamp(execution.approval_expires_ms) },
        { key: 'deadline', label: '执行开始期限', children: exactTimestamp(execution.start_deadline_ms) }, { key: 'cancelTime', label: '取消请求时间', children: exactTimestamp(execution.cancel_requested_ms) },
      ]} />
      {['cancelled_result_unknown', 'result_unknown'].includes(execution.status) && <Alert className="error-notice" showIcon type="warning" title={executionStates[execution.status]} description={execution.status === 'cancelled_result_unknown' ? '后续步骤已经停止；已预留或已发送的原命令继续保留反馈，可按当前许可核对。' : '自动执行已经停止，请查询原命令的持久反馈；核对取得明确结果后再处理剩余步骤。'} />}
      <Form className="section-actions" layout="vertical"><Form.Item label="人工操作理由"><Input.TextArea aria-label="人工操作理由" maxLength={2000} rows={2} value={reason} onChange={event => setReason(event.target.value)} /></Form.Item><Space wrap>{allowed.map(value => <Button key={value.action} disabled={!value.allowed || busy || !reason.trim()} danger={value.action === 'cancel'} onClick={() => setAction(value.action as 'cancel' | 'reconcile' | 'resume')}>{controlActionNames[value.action ?? ''] || value.action}</Button>)}</Space>{allowed.filter(value => !value.allowed).map(value => <small className="table-subtext" key={value.action}>{controlActionNames[value.action ?? ''] || value.action}：{value.reason || '当前状态未提供此操作'}</small>)}{lastRequest && activeOperation?.status === 'pending' && <Button className="section-actions" disabled={busy} onClick={() => void intervene(true)}>重复提交原操作请求</Button>}</Form>
      <Tabs items={[
        { key: 'steps', label: '会签与设备步骤', children: <><h3>人员会签</h3><Table size="small" pagination={false} rowKey={row => `${row.user_id}:${row.role}`} dataSource={execution.approvals ?? []} columns={[{ title: '人员 / 角色', render: (_, row) => `${row.user_id} / ${row.role}` }, { title: '确认时间', render: (_, row) => exactTimestamp(row.at_ms) }, { title: '现场证据', render: (_, row) => row.local && row.step_up ? '现场入口与二次认证' : '账号会话' }]} /><Space wrap className="section-actions">{!user.ai && ['awaiting_approval', 'approved'].includes(execution.status) && user.roles.filter(role => role in roleNames).map(role => <Button key={role} disabled={busy || (role === 'safety' && !local)} onClick={() => void approve(role)}>{roleNames[role]}</Button>)}{manage && execution.status === 'approved' && <Button type="primary" disabled={busy} onClick={() => void dispatch()}>正式下发</Button>}</Space><h3>设备动作与回执</h3><Table size="small" pagination={false} rowKey={row => row.command_id || row.step_id || ''} dataSource={execution.steps ?? []} scroll={{ x: 850 }} columns={[
          { title: '步骤 / 命令身份', render: (_, row) => <>{row.step_id}<small className="table-subtext">{row.command_id}</small></> }, { title: '设备 / 分支', render: (_, row) => `${[...(rule.data?.policy?.steps ?? []), ...(rule.data?.policy?.degraded ?? [])].find(value => value.id === row.step_id)?.device_id || '未取得对应预案步骤'} / ${execution.branch || '正常'}` }, { title: '设备结果', render: (_, row) => <StateTag status={row.status ?? ''} /> }, { title: '步骤开始 / 保存结果时间', render: (_, row) => <>{exactTimestamp(row.started_ms)}<small className="table-subtext">{exactTimestamp(row.finished_ms)}</small></> }, { title: '反馈说明', dataIndex: 'message' },
        ]} /><Collapse className="section-card" items={[{ key: 'policy', label: '采用版本的条件、正常步骤和降级步骤', children: <pre className="json-view">{rule.data ? json({ definition_id: rule.data.id, version: rule.data.version, effective_ms: rule.data.effective_ms, policy: rule.data.policy }) : rule.loading ? '正在读取采用版本' : '未取得对应预案版本'}</pre> }, { key: 'binding', label: '执行记录绑定摘要', children: <pre className="json-view">{json(execution.binding)}</pre> }, { key: 'snapshot', label: '判断时的现场观测与来源', children: <pre className="json-view">{json(execution.snapshot)}</pre> }]} /></> },
        { key: 'operations', label: '人工请求与回执', children: <Table<Contract<'ControlOperation'>> size="small" rowKey={row => row.id ?? ''} pagination={false} dataSource={detail.data.operations ?? []} scroll={{ x: 1150 }} expandable={{ expandedRowRender: row => <pre className="json-view">{json(row)}</pre> }} columns={[
          { title: '操作身份 / 动作', render: (_, row) => <>{row.id}<small className="table-subtext">{controlActionNames[row.action ?? ''] || row.action}</small></> }, { title: '人工操作状态', render: (_, row) => operationStatusText(row) }, { title: '发起 / 目标节点', render: (_, row) => `${row.origin_node_id} / ${row.target_node_id}` }, { title: '请求 / 来源 / 结果版本', render: (_, row) => `${String(row.expected_version)} / ${String(row.expected_source_version)} / ${String(row.result_version ?? '尚未返回')}` }, { title: '请求 / 开始 / 完成时间', render: (_, row) => <>{exactTimestamp(row.requested_ms)}<small className="table-subtext">{exactTimestamp(row.started_ms)}<br />{exactTimestamp(row.processed_ms)}</small></> }, { title: '操作者 / 理由 / 原因', render: (_, row) => <>{row.actor?.user_id}<small className="table-subtext">{row.reason}<br />{row.error}</small></> },
        ]} /> },
        { key: 'evidence', label: '原命令核对证据', children: <Table<Contract<'CommandEvidence'>> size="small" rowKey={row => row.id ?? ''} pagination={false} dataSource={detail.data.evidence ?? []} scroll={{ x: 1050 }} expandable={{ expandedRowRender: row => <pre className="json-view">{json(row)}</pre> }} columns={[
          { title: '证据 / 命令身份', render: (_, row) => <>{row.id}<small className="table-subtext">{row.command_id}</small></> }, { title: '设备 / 步骤', render: (_, row) => `${row.device_id} / ${row.step_id}` }, { title: '反馈来源', dataIndex: 'source' }, { title: '设备观测 / 平台取得时间', render: (_, row) => <>{exactTimestamp(row.observed_ms)}<small className="table-subtext">{exactTimestamp(row.collected_ms)}</small></> }, { title: '结果 / 信任判断', render: (_, row) => <><StateTag status={row.status ?? ''} /><small className="table-subtext">{row.trusted ? '反馈绑定核对通过' : row.rejection || '反馈尚未核对通过'}</small></> }, { title: '回执说明', dataIndex: 'message' },
        ]} /> },
        { key: 'timeline', label: '关联时间线', children: <Timeline items={(detail.data.timeline ?? []).map(row => ({ key: row.id, content: <div><strong>{eventNames[row.kind ?? ''] || row.kind} · {exactTimestamp(row.at_ms)}</strong><p>{row.transition ? `${executionStates[row.transition.from ?? ''] || row.transition.from} → ${executionStates[row.transition.to ?? ''] || row.transition.to}` : row.message || row.step?.message || row.evidence?.message || '记录已保存'}</p><small className="table-subtext">来源：{row.source || '未记录'} · 人员：{row.actor?.user_id || '未记录'} · 本地/来源版本：{String(row.transition?.version ?? '未记录')} / {String(row.transition?.source_version ?? '未记录')}<br />命令：{row.transition?.command_id || row.step?.command_id || row.evidence?.command_id || '未关联'} · 追踪：{row.trace_id || row.transition?.trace_id || '未记录'}</small><Collapse items={[{ key: row.id ?? '', label: '查看记录依据', children: <pre className="json-view">{json(row)}</pre> }]} /></div> }))} /> },
      ]} />
      </div>}
    </Drawer>
    <Modal title={controlActionNames[action ?? ''] || '人工操作'} open={!!action && !!execution} onCancel={() => setAction(undefined)} onOk={() => void intervene()} confirmLoading={busy} okText="提交此次人工操作"><p>{action === 'cancel' ? '停止尚未执行的后续步骤，已发送的原命令继续保留结果与核对记录。' : action === 'reconcile' ? '只读查询原命令持久反馈，核对命令、设备、内容和来源；没有明确可信结果时继续等待。' : '使用已核对结果继续剩余步骤，原命令身份与已完成步骤保持在执行记录中。'}</p><Descriptions column={1} items={[{ key: 'reason', label: '此次理由', children: reason }, { key: 'version', label: '当前 / 来源版本', children: `${String(execution?.version ?? '')} / ${String(detail.data?.source_version ?? '')}` }]} /></Modal>
  </>;
}
