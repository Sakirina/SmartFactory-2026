import { useState } from 'react';
import { Alert, App, Button, Card, Descriptions, Drawer, Form, Input, InputNumber, Modal, Progress, Select, Space, Switch, Table, Tag } from 'antd';
import { json, post, timestamp, useResource } from './api';
import type { Alarm, Entity, Json, User } from './types';

const canManage = (user: User) => !user.ai && user.roles.some(role => ['admin', 'engineer'].includes(role));
const errorText = (error: unknown) => error instanceof Error ? error.message : String(error);
const localDateTime = (ms: number) => new Date(ms - new Date(ms).getTimezoneOffset() * 60000).toISOString().slice(0, 16);
const names: Record<string, string> = { pending: '等待处理', running: '正在计算', completed: '已完成', failed: '处理失败', approved: '已批准', rejected: '已拒绝', delivered: '已送达', sending: '等待发送确认', result_unknown: '结果待核对', suppressed: '未匹配到收件人', delegated_to_cloud: '由云端发送', healthy: '正常' };
function Status({ value }: { value: string }) { return <Tag color={['failed', 'rejected'].includes(value) ? 'error' : ['result_unknown', 'pending'].includes(value) ? 'warning' : ['completed', 'delivered', 'approved', 'healthy'].includes(value) ? 'success' : 'default'}>{names[value] || value || '等待首次运行'}</Tag>; }
function Heading({ title, children, actions }: { title: string; children: React.ReactNode; actions?: React.ReactNode }) { return <div className="page-heading"><div><h1>{title}</h1><p>{children}</p></div><div className="heading-actions">{actions}</div></div>; }
function Failure({ error }: { error: string }) { return error ? <Alert type="error" showIcon title="本次更新未完成" description={error} className="error-notice" /> : null; }

interface Job { id: string; device_id: string; status: string; progress: number; from_ms: number; to_ms: number; reason: string; error?: string }
export function Jobs({ user, refreshMS }: { user: User; refreshMS: number }) {
  const jobs = useResource<Job[]>('/jobs', refreshMS);
  const devices = useResource<Entity[]>('/entities');
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [form] = Form.useForm();
  const { message } = App.useApp();
  const submit = async () => {
    setBusy(true);
    try {
      const values = await form.validateFields();
      await post('/jobs', { device_id: values.device_id, from_ms: new Date(values.from).getTime(), to_ms: new Date(values.to).getTime(), reason: values.reason });
      setOpen(false); await jobs.reload(); void message.success('补算任务已创建');
    } catch (error) { void message.error(errorText(error)); } finally { setBusy(false); }
  };
  const retry = async (job: Job) => { try { await post(`/jobs/${encodeURIComponent(job.id)}/retry`); await jobs.reload(); } catch (error) { void message.error(errorText(error)); } };
  return <><Heading title="历史补算" actions={<Space><Button onClick={() => void jobs.reload()}>更新任务</Button><Button type="primary" disabled={!canManage(user)} onClick={() => { form.setFieldsValue({ from: localDateTime(Date.now() - 3600000), to: localDateTime(Date.now()), reason: '' }); setOpen(true); }}>创建补算任务</Button></Space>}>查看补传影响的时间范围、计算进度与失败原因，完成后可在数据监控中查看修订。</Heading><Failure error={jobs.error || devices.error} /><Card><Table rowKey="id" dataSource={jobs.data ?? []} loading={jobs.loading} scroll={{ x: 850 }} columns={[
    { title: '设备', dataIndex: 'device_id' }, { title: '状态', dataIndex: 'status', render: value => <Status value={value} /> },
    { title: '进度', dataIndex: 'progress', width: 150, render: (value, job) => <Progress percent={Math.round(Number(value || 0) * 100)} status={job.status === 'failed' ? 'exception' : undefined} size="small" /> },
    { title: '影响范围', render: (_, job) => <div>{timestamp(job.from_ms)}<small className="table-subtext">至 {timestamp(job.to_ms)}</small></div> },
    { title: '原因与错误', render: (_, job) => job.error || job.reason },
    { title: '操作', render: (_, job) => <Space><Button type="link" onClick={() => { location.hash = 'data'; }}>查看数据</Button>{job.status === 'failed' && <Button disabled={!canManage(user)} onClick={() => void retry(job)}>重新计算</Button>}</Space> },
  ]} /></Card><Modal title="创建历史补算任务" open={open} onCancel={() => setOpen(false)} onOk={() => void submit()} confirmLoading={busy} okText="开始补算"><Form form={form} layout="vertical"><Form.Item label="设备" name="device_id" rules={[{ required: true }]}><Select options={(devices.data ?? []).filter(device => device.kind === 'device').map(device => ({ value: device.id, label: device.name }))} /></Form.Item><Form.Item label="开始时间" name="from" rules={[{ required: true }]}><Input type="datetime-local" /></Form.Item><Form.Item label="结束时间" name="to" rules={[{ required: true }]}><Input type="datetime-local" /></Form.Item><Form.Item label="补算原因" name="reason" rules={[{ required: true }]}><Input.TextArea rows={3} /></Form.Item></Form></Modal></>;
}

interface Notice { id: string; notification_id: string; user_id: string; entity_id: string; channel: string; status: string; reason?: string; at_ms: number; alarm: Alarm }
export function Notifications({ user, refreshMS }: { user: User; refreshMS: number }) {
  const data = useResource<Notice[]>('/notifications', refreshMS);
  const [selected, setSelected] = useState<Notice | null>(null);
  const { message } = App.useApp();
  const acknowledge = async (notice: Notice) => { try { await post(`/alarms/${encodeURIComponent(notice.alarm.id)}/acknowledge`); setSelected(null); await data.reload(); void message.success('告警人员确认已记录'); } catch (error) { void message.error(errorText(error)); } };
  return <><Heading title="通知记录" actions={<Button onClick={() => void data.reload()}>更新通知</Button>}>按人员和渠道查看告警通知、发送结果与处理依据。</Heading><Failure error={data.error} /><Card><Table rowKey="id" dataSource={data.data ?? []} scroll={{ x: 750 }} columns={[
    { title: '发生时间', dataIndex: 'at_ms', render: timestamp }, { title: '设备 / 规则', render: (_, notice) => <div>{notice.entity_id}<small className="table-subtext">{notice.alarm.definition_id}</small></div> },
    { title: '收件人', dataIndex: 'user_id' }, { title: '渠道', dataIndex: 'channel', render: value => ({ in_app: '站内', email: '邮件', sms: '短信' }[value as string] || value) },
    { title: '发送状态', dataIndex: 'status', render: value => <Status value={value} /> }, { title: '操作', render: (_, notice) => <Button type="link" onClick={() => setSelected(notice)}>查看通知</Button> },
  ]} /></Card><Drawer title="通知与告警依据" open={!!selected} onClose={() => setSelected(null)} width={Math.min(620, window.innerWidth)}>{selected && <><Descriptions column={1} items={[{ key: 'state', label: '发送状态', children: <Status value={selected.status} /> }, { key: 'reason', label: '处理说明', children: selected.reason || '发送过程正常' }, { key: 'alarm', label: '告警状态', children: selected.alarm.active ? '异常持续中' : '异常已恢复' }, { key: 'value', label: '触发值', children: json(selected.alarm.value) }, { key: 'identity', label: '关联通知', children: selected.notification_id }]} />{selected.status === 'result_unknown' && <Alert type="warning" showIcon title="发送结果需要核对" description="请根据通知标识在渠道服务中查询投递记录，确认后由人员处置。" />}<Button className="section-actions" disabled={!user.roles.some(role => ['admin', 'engineer', 'leader', 'safety'].includes(role)) || user.ai} onClick={() => void acknowledge(selected)}>记录告警人员确认</Button></>}</Drawer></>;
}

interface Proposal { id: string; node_id: string; entity: Entity; status: string; reason?: string; version: number; base_version: number; created_ms: number }
export function AssetProposals({ user, mode }: { user: User; mode: string }) {
  const data = useResource<Proposal[]>('/asset-proposals', 5000);
  const [selected, setSelected] = useState<Proposal | null>(null);
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const { message } = App.useApp();
  const decide = async (approve: boolean) => { if (!selected || !reason.trim()) return; setBusy(true); try { await post(`/asset-proposals/${encodeURIComponent(selected.id)}/decide`, { approve, reason, expected_version: selected.version }); setSelected(null); await data.reload(); void message.success('评审结果已保存'); } catch (error) { void message.error(errorText(error)); } finally { setBusy(false); } };
  return <Card title="现场资产变更申请" className="section-card"><Failure error={data.error} /><Table rowKey="id" dataSource={data.data ?? []} scroll={{ x: 640 }} columns={[
    { title: '资产', render: (_, proposal) => proposal.entity.name }, { title: '申请节点', dataIndex: 'node_id' }, { title: '状态', dataIndex: 'status', render: value => <Status value={value} /> }, { title: '申请时间', dataIndex: 'created_ms', render: timestamp }, { title: '处理意见', dataIndex: 'reason' },
    { title: '操作', render: (_, proposal) => <Button type="link" onClick={() => { setSelected(proposal); setReason(''); }}>查看申请</Button> },
  ]} /><Modal title="资产变更评审" open={!!selected} onCancel={() => setSelected(null)} footer={mode === 'cloud' && selected?.status === 'pending' && canManage(user) ? <Space><Button danger disabled={!reason.trim()} loading={busy} onClick={() => void decide(false)}>拒绝申请</Button><Button type="primary" disabled={!reason.trim()} loading={busy} onClick={() => void decide(true)}>批准申请</Button></Space> : <Button onClick={() => setSelected(null)}>关闭</Button>}><Descriptions column={1} items={[{ key: 'version', label: '原资产版本', children: selected?.base_version }, { key: 'node', label: '来源节点', children: selected?.node_id }]} /><pre className="json-view">{json(selected?.entity)}</pre>{mode === 'cloud' && selected?.status === 'pending' && <Form layout="vertical"><Form.Item label="评审意见" required><Input.TextArea value={reason} onChange={event => setReason(event.target.value)} rows={3} /></Form.Item></Form>}</Modal></Card>;
}

interface PluginSpec { id: string; name: string; kind: string; executable: string; args: string[]; poll_ms: number; enabled: boolean; credential_id?: string; push_credential_id?: string; config: Record<string, Json>; version: number }
interface PluginEntry { spec: PluginSpec; status: { state: string; last_ms: number; sequence: number; error?: string } }
export function Plugins() {
  const data = useResource<PluginEntry[]>('/plugins', 5000);
  const [editing, setEditing] = useState<PluginSpec | null>(null);
  const [form] = Form.useForm();
  const { message } = App.useApp();
  const open = (spec?: PluginSpec) => { const value = spec ?? { id: '', name: '', kind: 'organization', executable: '', args: [], poll_ms: 60000, enabled: false, config: {}, version: 0 }; setEditing(value); form.setFieldsValue({ ...value, args_json: json(value.args), config_json: json(value.config) }); };
  const save = async () => { if (!editing) return; try { const { args_json, config_json, ...values } = await form.validateFields(); const plugin = { ...editing, ...values, args: JSON.parse(args_json), config: JSON.parse(config_json) }; await post('/plugins', { plugin, expected_version: editing.version }); setEditing(null); await data.reload(); void message.success('插件配置已保存'); } catch (error) { void message.error(errorText(error)); } };
  return <><Heading title="同步插件" actions={<Button type="primary" onClick={() => open()}>登记同步插件</Button>}>组织插件通过独立进程运行，以推送处理变更，并按设定周期查询遗漏记录。</Heading><Failure error={data.error} /><Card><Table rowKey={entry => entry.spec.id} dataSource={data.data ?? []} scroll={{ x: 800 }} columns={[
    { title: '插件', render: (_, entry) => <div>{entry.spec.name}<small className="table-subtext">{entry.spec.id}</small></div> }, { title: '启用状态', render: (_, entry) => <Tag>{entry.spec.enabled ? '已启用' : '已停用'}</Tag> }, { title: '运行状态', render: (_, entry) => <Status value={entry.status.state} /> },
    { title: '最后运行', render: (_, entry) => timestamp(entry.status.last_ms) }, { title: '已同步序号', render: (_, entry) => entry.status.sequence || 0 }, { title: '失败原因', render: (_, entry) => entry.status.error }, { title: '操作', render: (_, entry) => <Button type="link" onClick={() => open(entry.spec)}>配置</Button> },
  ]} /></Card><Drawer title="组织同步插件" open={!!editing} onClose={() => setEditing(null)} width={Math.min(620, window.innerWidth)} extra={<Button type="primary" onClick={() => void save()}>保存插件</Button>}><Form form={form} layout="vertical"><Form.Item name="id" label="插件标识" rules={[{ required: true }]}><Input disabled={!!editing?.id} /></Form.Item><Form.Item name="name" label="插件名称" rules={[{ required: true }]}><Input /></Form.Item><Form.Item name="executable" label="已安装程序的绝对路径" rules={[{ required: true }]}><Input /></Form.Item><Form.Item name="args_json" label="启动参数"><Input.TextArea rows={2} /></Form.Item><Form.Item name="config_json" label="业务来源配置"><Input.TextArea rows={6} /></Form.Item><Form.Item name="credential_id" label="查询凭据条目标识"><Input /></Form.Item><Form.Item name="push_credential_id" label="推送凭据条目标识"><Input /></Form.Item><Form.Item name="poll_ms" label="补偿查询周期（毫秒）"><InputNumber min={1000} max={86400000} /></Form.Item><Form.Item name="enabled" label="启用插件" valuePropName="checked"><Switch /></Form.Item></Form></Drawer></>;
}
