import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Descriptions, Form, Input, Modal, Select, Space, Table, Tabs, Tag } from 'antd';
import { RequestError } from './api';
import { simulateDefinitionDraft, validateDefinitionDraft, type Contract, type SimulationRequest, type SimulationResult } from './generated-client';
import { buildSimulationRequest, simulationReplay } from './simulation-input';
import { exactTimestamp, stringifyExactJSON } from './precision';
import type { Draft } from './types';

const exactJSON = (value: unknown) => stringifyExactJSON(value, 2);
const errorText = (error: unknown) => error instanceof Error ? error.message : String(error);
export function DraftSimulation({ draft, save, reloadRevision }: { draft: Draft; save: () => Promise<Draft | null>; reloadRevision: () => Promise<Draft | null> }) {
  const [open, setOpen] = useState(false);
  const [pointsText, setPointsText] = useState('');
  const [historySource, setHistorySource] = useState<'database_snapshot' | 'provided'>('database_snapshot');
  const [historyText, setHistoryText] = useState('[]');
  const [parametersText, setParametersText] = useState('{"clock":{},"initial_state":{},"timeout_ms":5000}');
  const [order, setOrder] = useState<'event_time' | 'provided'>('event_time');
  const [selectedRevision, setSelectedRevision] = useState<number>();
  const [busy, setBusy] = useState(false);
  const [stale, setStale] = useState(false);
  const [error, setError] = useState('');
  const [successful, setSuccessful] = useState<{ request: SimulationRequest; result: SimulationResult }>();
  const controller = useRef<AbortController | null>(null);
  useEffect(() => () => controller.current?.abort(), []);
  const prepare = async () => {
    setBusy(true); setError('');
    try {
      const saved = await save();
      if (!saved) { setError('草稿保存未完成，请查看编辑器的错误内容，再保存并校验当前草稿'); return; }
      const validation = await validateDefinitionDraft(saved);
      if (!validation.valid) throw new Error('草稿需要校验：' + validation.errors.join('；'));
      setSelectedRevision(saved.version); setStale(false); setSuccessful(undefined);
    } catch (error) { setError(errorText(error)); } finally { setBusy(false); }
  };
  const run = async () => {
    if (selectedRevision === undefined) { setError('请保存并校验当前草稿，选定可模拟的修订'); return; }
    setBusy(true); setError('');
    const active = new AbortController(); controller.current = active;
    try {
      const request = buildSimulationRequest(pointsText, historySource, historyText, parametersText, order);
      const result = await simulateDefinitionDraft(draft.id, selectedRevision, request, active.signal);
      if (!active.signal.aborted) { setSuccessful({ request, result }); setStale(false); }
    } catch (error) {
      if (!active.signal.aborted) {
        if (error instanceof RequestError && error.status === 409) { setStale(true); setError(`草稿 ${draft.id} 的修订 ${selectedRevision} 已发生变化，请重新读取当前修订、校验草稿并选择模拟修订。连续输入和模拟参数仍保存在本页。服务返回：${error.message}`); }
        else setError(errorText(error));
      }
    } finally { if (controller.current === active) controller.current = null; setBusy(false); }
  };
  const close = () => { controller.current?.abort(); setOpen(false); };
  const refreshRevision = async () => {
    setBusy(true);
    try { const latest = await reloadRevision(); if (latest) { setSelectedRevision(undefined); setStale(false); setError(`已读取草稿当前修订 v${latest.version}，编辑内容和连续输入已保留，请保存并校验当前草稿。`); } }
    catch (error) { setError(errorText(error)); }
    finally { setBusy(false); }
  };
  const result = successful?.result;
  const exportReplay = () => {
    if (!successful) return;
    const url = URL.createObjectURL(new Blob([exactJSON(simulationReplay(draft.id, successful.request, successful.result))], { type: 'application/json' }));
    const link = document.createElement('a'); link.href = url; link.download = 'simulation-' + draft.id.replace(/[^A-Za-z0-9_-]/g, '_') + '-v' + String(result?.draft_revision) + '.json'; link.click(); URL.revokeObjectURL(url);
  };
  return <><Button onClick={() => {
    const at = Date.now();
    setPointsText(exactJSON([0, 1, 2].map(index => ({ id: `input-${index + 1}`, device_id: draft.definition.selector.device_ids[0] || '', key: draft.definition.selector.keys[0] || '', value: 26 + index, quality: 'GOOD', observed_ms: at + index * 1000, source_sequence: index + 1, time_source: 'simulation', unit: '' }))));
    setSelectedRevision(draft.version > 0 ? draft.version : undefined); setSuccessful(undefined); setError(''); setStale(false); setOpen(true);
  }}>模拟运行</Button><Modal title="草稿模拟运行" open={open} onCancel={close} width={1150} styles={{ body: { maxHeight: 'calc(100vh - 230px)', overflowY: 'auto' } }} footer={<Space wrap><Button onClick={close}>关闭</Button><Button disabled={!successful} onClick={exportReplay}>导出复现记录</Button><Button loading={busy} onClick={() => void prepare()}>保存并校验当前草稿</Button><Button type="primary" loading={busy} disabled={selectedRevision === undefined || stale} onClick={() => void run()}>运行模拟</Button></Space>}>
    <p>连续模拟采用服务端已保存的草稿修订；每条输入按照选择的时序推进各实体的节点状态，结果提供采用的历史、时钟和资产版本。</p>
    <Descriptions size="small" column={2} items={[{ key: 'id', label: '草稿身份', children: draft.id }, { key: 'revision', label: '选择的草稿修订', children: selectedRevision === undefined ? '尚未选择，请保存并校验' : `v${selectedRevision}` }]} />
    <Form layout="vertical" className="section-card"><Form.Item label="连续输入 JSON（Unix 毫秒、质量与单位保留在每条输入中）"><Input.TextArea aria-label="模拟输入样本" value={pointsText} onChange={event => setPointsText(event.target.value)} rows={12} className="code-input" /></Form.Item>
      <div className="simulation-settings"><Form.Item label="输入时序"><Select aria-label="模拟输入时序" value={order} onChange={setOrder} options={[{ value: 'event_time', label: '观测时间、来源序号、输入身份' }, { value: 'provided', label: '输入数组顺序' }]} /></Form.Item><Form.Item label="历史来源"><Select aria-label="模拟历史来源" value={historySource} onChange={setHistorySource} options={[{ value: 'database_snapshot', label: '由服务取得历史快照' }, { value: 'provided', label: '自行提供历史（空数组表示空历史）' }]} /></Form.Item></div>
      {historySource === 'provided' && <Form.Item label="自行提供的历史 JSON"><Input.TextArea aria-label="模拟历史输入" value={historyText} onChange={event => setHistoryText(event.target.value)} rows={5} className="code-input" /></Form.Item>}
      <Form.Item label="模拟参数 JSON（clock、initial_state、asset_versions、timeout_ms）"><Input.TextArea aria-label="模拟参数" value={parametersText} onChange={event => setParametersText(event.target.value)} rows={5} className="code-input" /></Form.Item>
    </Form>
    {error && <Alert type={stale ? 'warning' : 'error'} showIcon title={stale ? '草稿修订需要重新校验' : '模拟未完成'} description={error} action={stale ? <Button disabled={busy} onClick={() => void refreshRevision()}>读取当前修订并保留编辑内容</Button> : undefined} />}
    {result && <div className="section-card" data-testid="simulation-result"><Descriptions bordered size="small" column={2} items={[
      { key: 'revision', label: '实际草稿修订', children: String(result.draft_revision) }, { key: 'order', label: '实际时序', children: result.order }, { key: 'plan', label: '计划身份', children: result.plan_id }, { key: 'planHash', label: '计划摘要', children: result.plan_sha256 }, { key: 'inputHash', label: '输入摘要', children: result.input_sha256 }, { key: 'history', label: '实际历史来源', children: result.history_source === 'provided' ? '自行提供的历史' : '服务取得的历史快照' }, { key: 'historyHash', label: '历史摘要', children: result.history_sha256 }, { key: 'timeout', label: '计算时间预算', children: `${String(result.timeout_ms)} ms` },
    ]} />
    <h4 className="section-card">连续输入结果</h4><Table<Contract<'SimulationStep'>> rowKey={(_row, index) => String(index)} dataSource={result.results ?? []} pagination={false} scroll={{ x: 950 }} columns={[
      { title: '输入身份 / 原数组位置', render: (_, step) => <>{step.point?.id}<small className="table-subtext">{String(step.input_index)}</small></> },
      { title: '设备 / 测点', render: (_, step) => <>{step.point?.device_id}<small className="table-subtext">{step.point?.key}</small></> },
      { title: '观测时间 / 时间来源', render: (_, step) => <>{exactTimestamp(step.point?.observed_ms)}<small className="table-subtext">{step.point?.time_source || '未提供'}</small></> },
      { title: '输入值 / 单位 / 质量', render: (_, step) => <><code>{exactJSON(step.point?.value)}</code><small className="table-subtext">{step.point?.unit || '未提供单位'} · {step.point?.quality}</small></> },
      { title: '逻辑时钟 / 新鲜度参照', render: (_, step) => <>{String(step.clock_ms)} ms<small className="table-subtext">{String(step.freshness_at_ms)} ms · 阈值 {String(step.freshness_ms)} ms</small></> },
      { title: '计算输出 / 触发', render: (_, step) => <><pre>{exactJSON(step.evaluation?.values)}</pre><Tag>{step.evaluation?.trigger ? '满足触发条件' : '未满足触发条件'}</Tag></> },
    ]} expandable={{ expandedRowRender: step => <><h4>节点输出</h4><Table<Contract<'NodeTrace'>> rowKey={(_row, index) => String(index)} pagination={false} dataSource={step.evaluation?.trace ?? []} scroll={{ x: 800 }} columns={[
      { title: '节点 / 类型', render: (_, trace) => <>{trace.node_id}<small className="table-subtext">{trace.type}</small></> }, { title: '输入', render: (_, trace) => <pre>{exactJSON(trace.input)}</pre> }, { title: '输出', render: (_, trace) => <pre>{exactJSON(trace.output)}</pre> }, { title: '状态变化', render: (_, trace) => <pre>{exactJSON({ before: trace.before, after: trace.after })}</pre> }, { title: '处理结果', render: (_, trace) => trace.error || (trace.skipped ? '已跳过' : '已计算') },
    ]} /><h4>质量统计</h4><pre className="json-view">{exactJSON(step.evaluation?.quality)}</pre></> }} />
    <Tabs className="section-card" items={[{ key: 'state', label: '最终节点状态', children: <pre className="json-view" data-testid="simulation-final-state">{exactJSON(result.final_state)}</pre> }, { key: 'history', label: '采用的历史快照', children: <pre className="json-view">{exactJSON(result.history)}</pre> }, { key: 'assets', label: '采用的资产版本', children: <pre className="json-view">{exactJSON(result.asset_versions)}</pre> }]} />
    </div>}
  </Modal></>;
}
