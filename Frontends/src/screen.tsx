import { useCallback, useMemo, useState } from 'react';
import { Alert, App, Button, Descriptions, Drawer, Form, Input, Modal, Select, Space, Switch, Table, Tag } from 'antd';
import { post, shortTime, timestamp, useResource } from './api';
import { Trend } from './chart';
import { Metric, Quality, RevisionModal, StateTag } from './views';
import type { Alarm, Dashboard, DataResult, Entity, Point, Revision, Source, User } from './types';

const emptyDashboard = (refreshMS: number): Dashboard => ({ id: '', title: '示例工厂 · 生产安全运行', group_id: 'factory', version: 0, device_ids: [], keys: [], metrics: [], window_ms: 3600000, refresh_ms: refreshMS, show_sources: true, show_alarms: true });
type Detail = { kind: 'point'; device: string; key: string; label: string } | { kind: 'source'; source: Source } | { kind: 'alarm'; alarm: Alarm };

export function Screen({ user, refreshMS, mode, onLogout }: { user: User; refreshMS: number; mode: string; onLogout: () => Promise<void> }) {
  const saved = useResource<Dashboard[]>('/dashboards');
  const entities = useResource<Entity[]>('/entities');
  const [selectedID, setSelectedID] = useState(localStorage.getItem('sf.screen.selected') || 'factory');
  const dashboard = saved.data?.find(d => d.id === selectedID) ?? saved.data?.[0] ?? emptyDashboard(refreshMS);
  const query = new URLSearchParams({ limit: '2000', window_ms: String(dashboard.window_ms), include_latest: 'true' });
  const devices = [...new Set([...dashboard.device_ids, ...dashboard.metrics.map(m => m.device_id)])];
  if (devices.length) query.set('device_ids', devices.join(','));
  if (dashboard.keys.length) query.set('keys', [...new Set([...dashboard.keys, ...dashboard.metrics.map(m => m.key)])].join(','));
  const interval = dashboard.refresh_ms;
  const data = useResource<DataResult>('/data?' + query, interval);
  const overview = useResource<{ entities: number; active_alarms: number; sources: Source[]; pending_deliveries: number }>('/overview', interval);
  const alarms = useResource<Alarm[]>('/alarms', interval);
  const [editing, setEditing] = useState<Dashboard | null>(null);
  const [saving, setSaving] = useState(false);
  const [detail, setDetail] = useState<Detail | null>(null);
  const [revision, setRevision] = useState<Revision | null>(null);
  const revise = useCallback((value: Revision) => setRevision(value), []);
  const [form] = Form.useForm<Dashboard>();
  const { message } = App.useApp();
  const canEdit = mode === 'cloud' && !user.ai && user.roles.some(role => ['admin', 'engineer'].includes(role));
  const entityNames = new Map((entities.data ?? []).map(entity => [entity.id, entity.name]));
  const latest = useMemo(() => {
    const points = new Map<string, Point>();
    for (const point of data.data?.latest ?? data.data?.points ?? []) {
      const id = point.device_id + '/' + point.key;
      if (!points.has(id) || points.get(id)!.observed_ms < point.observed_ms) points.set(id, point);
    }
    return points;
  }, [data.data]);
  const metrics = dashboard.metrics.length ? dashboard.metrics : [...latest.values()].slice(0, 6).map(p => ({ device_id: p.device_id, key: p.key, label: entityNames.get(p.device_id) + ' / ' + p.key }));
  const openEditor = (create = false) => {
    const d = create ? { ...dashboard, id: '', title: '新的运行大屏', version: 0 } : { ...dashboard };
    setEditing(d); form.setFieldsValue(d);
  };
  const save = async () => {
    try {
      const values = await form.validateFields();
      setSaving(true);
      const result = await post<Dashboard>('/dashboards', { dashboard: { ...editing, ...values }, expected_version: editing?.version ?? 0 });
      setSelectedID(result.id); localStorage.setItem('sf.screen.selected', result.id);
      await saved.reload(); setEditing(null); void message.success('大屏配置已保存，其他页面可读取此版本');
    } catch (error) { void message.error(error instanceof Error ? error.message : '请检查配置内容'); }
    finally { setSaving(false); }
  };
  const refresh = () => { void data.reload(); void overview.reload(); void alarms.reload(); };
  return <div className="screen-page">
    <header className="screen-header"><div><span className="screen-kicker">SMARTFACTORY / OPERATIONS</span><h1>{dashboard.title}</h1></div><Space wrap>
      <span>{timestamp(overview.updatedAt)} · v{dashboard.version}</span>
      {(saved.data?.length ?? 0) > 1 && <Select aria-label="选择运行大屏" value={dashboard.id} style={{ minWidth: 180 }} options={saved.data!.map(d => ({ value: d.id, label: d.title }))} onChange={id => { setSelectedID(id); localStorage.setItem('sf.screen.selected', id); }} />}
      <Button ghost onClick={refresh}>更新数据</Button>
      {canEdit && <Button ghost onClick={() => openEditor()}>配置大屏</Button>}
      <Button ghost onClick={() => void onLogout()}>退出</Button>
    </Space></header>
    {(saved.error || data.error || overview.error) && <Alert type="warning" showIcon title="数据更新暂时中断，当前画面保留最近一次结果" description={saved.error || data.error || overview.error} />}
    <div className="metric-grid"><Metric title="设备与资产" value={overview.data?.entities ?? 0} note="当前账号可访问范围" /><Metric title="持续异常" value={overview.data?.active_alarms ?? 0} note="按规则、实体与异常周期合并" tone="amber" /><Metric title="有效观测" value={data.data?.quality.good ?? 0} note="当前趋势查询范围" /><Metric title="在线数据来源" value={(overview.data?.sources ?? []).filter(s => s.status === 'online').length} note={`${overview.data?.sources?.length ?? 0} 个已报告来源`} tone="green" /></div>
    <div className="screen-main"><section className="screen-panel"><div className="screen-panel-title">生产过程趋势<span>实时观测 / 质量与修订标记</span></div><Trend data={data.data} height={440} dark onRevision={revise} /><Quality data={data.data} /></section>
      <section className="screen-panel"><div className="screen-panel-title">现场指标<span>选择指标查看记录</span></div><div className="live-indicators">{metrics.map(metric => {
        const point = latest.get(metric.device_id + '/' + metric.key);
        const value = point?.value;
        return <button className="indicator-button" key={metric.device_id + '/' + metric.key} onClick={() => setDetail({ kind: 'point', device: metric.device_id, key: metric.key, label: metric.label })}>
          <span>{metric.label || metric.device_id + ' / ' + metric.key}</span><strong>{typeof value === 'number' ? value.toLocaleString(undefined, { maximumFractionDigits: 2 }) : typeof value === 'boolean' ? value ? '开启' : '关闭' : value == null ? '暂无数据' : String(value)}<small>{point?.unit}</small></strong><StateTag status={point?.quality ?? 'unknown'} /><small>{point ? shortTime(point.observed_ms) : '等待观测'}</small>
        </button>;
      })}</div>{!metrics.length && <div className="screen-empty">等待现场观测</div>}</section></div>
    <div className="screen-bottom">{dashboard.show_sources && <section className="screen-panel"><div className="screen-panel-title">来源与补传状态</div><div className="source-cards">{(overview.data?.sources ?? []).map(source => <button className="source-button" key={source.id} onClick={() => setDetail({ kind: 'source', source })}><strong>{source.id}</strong><StateTag status={source.status} /><small>最近通信 {shortTime(source.last_seen_ms)} · 补传 {source.backfill}</small></button>)}</div></section>}
      {dashboard.show_alarms && <section className="screen-panel"><div className="screen-panel-title">异常观察</div><div className="screen-alarms">{(alarms.data ?? []).filter(a => a.active).slice(0, 4).map(alarm => <button className="alarm-button" key={alarm.id} onClick={() => setDetail({ kind: 'alarm', alarm })}><Tag color="orange">{alarm.severity}</Tag><span>{alarm.definition_id}</span><small>{entityNames.get(alarm.entity_id) ?? alarm.entity_id} · {shortTime(alarm.updated_ms)}</small></button>)}{!(alarms.data ?? []).some(a => a.active) && <p>当前没有持续中的告警</p>}</div></section>}</div>
    <footer>数据访问身份：{user.name}<span>选择现场指标、来源或告警可查看详情</span></footer>
    <Modal title="配置运行大屏" open={!!editing} onCancel={() => setEditing(null)} onOk={() => void save()} confirmLoading={saving} okText="保存大屏" width={720}>
      <Form form={form} layout="vertical"><Form.Item name="title" label="大屏标题" rules={[{ required: true, max: 200 }]}><Input /></Form.Item>
        <Form.Item name="group_id" label="所属资产" rules={[{ required: true }]}><Select options={(entities.data ?? []).filter(e => e.kind === 'asset').map(e => ({ value: e.id, label: e.name }))} /></Form.Item>
        <Form.Item name="device_ids" label="趋势设备"><Select mode="multiple" maxCount={20} options={(entities.data ?? []).filter(e => e.kind === 'device').map(e => ({ value: e.id, label: e.name }))} placeholder="留空时展示可访问设备" /></Form.Item>
        <Form.Item name="keys" label="趋势测点"><Select mode="tags" maxCount={20} placeholder="输入测点名称，留空展示全部测点" /></Form.Item>
        <Space align="start" wrap><Form.Item name="window_ms" label="趋势时间范围"><Select style={{ width: 180 }} options={[{ value: 60000, label: '最近 1 分钟' }, { value: 3600000, label: '最近 1 小时' }, { value: 86400000, label: '最近 24 小时' }]} /></Form.Item><Form.Item name="refresh_ms" label="刷新方式"><Select style={{ width: 160 }} options={[{ value: -1, label: '实时推送' }, { value: 1000, label: '每秒刷新' }, { value: 5000, label: '每 5 秒刷新' }, { value: 0, label: '手动更新' }]} /></Form.Item><Form.Item name="show_sources" label="来源面板" valuePropName="checked"><Switch /></Form.Item><Form.Item name="show_alarms" label="告警面板" valuePropName="checked"><Switch /></Form.Item></Space>
        <Form.List name="metrics">{(fields, { add, remove }) => <><h4>现场指标卡片</h4>{fields.map(field => <div className="dashboard-metric-editor" key={field.key}><Form.Item name={[field.name, 'device_id']} rules={[{ required: true, message: '请选择设备' }]}><Select aria-label={`指标 ${field.name + 1} 的设备`} placeholder="设备" options={(entities.data ?? []).map(e => ({ value: e.id, label: e.name }))} /></Form.Item><Form.Item name={[field.name, 'key']} rules={[{ required: true, message: '请输入测点' }]}><Input aria-label={`指标 ${field.name + 1} 的测点`} placeholder="测点" /></Form.Item><Form.Item name={[field.name, 'label']}><Input aria-label={`指标 ${field.name + 1} 的标题`} placeholder="显示名称" /></Form.Item><Button onClick={() => remove(field.name)}>删除</Button></div>)}<Button disabled={fields.length >= 12} onClick={() => add({ device_id: '', key: '', label: '' })}>添加指标</Button></>}</Form.List>
      </Form><Space className="section-actions"><Button onClick={() => openEditor(true)}>另存为新的大屏</Button><span className="muted">配置保存到平台，并随云端同步提供给现场。</span></Space>
    </Modal>
    <Drawer title={detail?.kind === 'point' ? detail.label : detail?.kind === 'source' ? '来源与补传详情' : '告警详情'} open={!!detail} width={Math.min(900, window.innerWidth)} onClose={() => setDetail(null)}>
      {detail?.kind === 'point' && <PointDetail device={detail.device} field={detail.key} refreshMS={interval} onRevision={revise} />}
      {detail?.kind === 'source' && <Descriptions column={1} items={[{ key: 'id', label: '来源', children: detail.source.id }, { key: 'status', label: '状态', children: <StateTag status={detail.source.status} /> }, { key: 'seen', label: '最近通信', children: timestamp(detail.source.last_seen_ms) }, { key: 'backfill', label: '补传状态', children: detail.source.backfill }, { key: 'reason', label: '原因', children: detail.source.reason || '暂无异常说明' }]} />}
      {detail?.kind === 'alarm' && <Descriptions column={1} items={[{ key: 'rule', label: '规则', children: detail.alarm.definition_id }, { key: 'entity', label: '设备或资产', children: detail.alarm.entity_id }, { key: 'level', label: '级别', children: detail.alarm.severity }, { key: 'status', label: '状态', children: detail.alarm.active ? '异常持续' : '已恢复' }, { key: 'ack', label: '人员确认', children: detail.alarm.acknowledged ? '已确认' : '等待确认' }, { key: 'start', label: '发生时间', children: timestamp(detail.alarm.started_ms) }, { key: 'last', label: '最近发生', children: timestamp(detail.alarm.updated_ms) }, { key: 'value', label: '触发值', children: JSON.stringify(detail.alarm.value) }, { key: 'version', label: '数据版本', children: detail.alarm.version }]} />}
    </Drawer><RevisionModal revision={revision} onClose={() => setRevision(null)} />
  </div>;
}

function PointDetail({ device, field, refreshMS, onRevision }: { device: string; field: string; refreshMS: number; onRevision: (r: Revision) => void }) {
  const params = new URLSearchParams({ device_ids: device, keys: field, limit: '2000', window_ms: '3600000' });
  const result = useResource<DataResult>('/data?' + params, refreshMS);
  return <>{result.error && <Alert type="error" title="数据查询未完成" description={result.error} />}<p>{device} / {field} · 最近 1 小时</p><Trend data={result.data} onRevision={onRevision} /><Quality data={result.data} /><Table size="small" rowKey="id" dataSource={result.data?.points ?? []} pagination={{ pageSize: 8 }} columns={[{ title: '观测时间', dataIndex: 'observed_ms', render: timestamp }, { title: '值', dataIndex: 'value', render: value => typeof value === 'object' ? JSON.stringify(value) : String(value) }, { title: '质量', dataIndex: 'quality', render: value => <StateTag status={value} /> }, { title: '原因', dataIndex: 'quality_reason' }]} /></>;
}
