import { useState } from 'react';
import { Alert, App, Button, Card, Drawer, Form, Input, Space, Switch, Table, Tag } from 'antd';
import { json, post, timestamp, useResource } from './api';
import type { Parameter } from './types';

const states: Record<string, string> = { applied: '已生效', pending: '等待程序确认', failed: '应用失败', restart_required: '需要重启' };
export function Configuration() {
  const data = useResource<Parameter[]>('/config', 5000);
  const [selected, setSelected] = useState<Parameter | null>(null);
  const [form] = Form.useForm();
  const [busy, setBusy] = useState(false);
  const { message } = App.useApp();
  const open = (parameter?: Parameter) => {
    const value: Parameter = parameter ?? { id: '', program: 'cloud', category: 'integration', description: '', schema: { type: 'object', required: ['token'], additionalProperties: false, properties: { token: { type: 'string' } } }, value: { token: '' }, dynamic: true, secret: true, version: 0, effective: {}, state: 'pending' };
    setSelected(value);
    form.setFieldsValue({ ...value, schema_json: json(value.schema), value_json: value.secret && value.version > 0 ? '' : json(value.value) });
  };
  const save = async () => {
    if (!selected) return;
    setBusy(true);
    try {
      const { schema_json, value_json, ...fields } = await form.validateFields();
      await post('/config', { parameter: { ...selected, ...fields, schema: JSON.parse(schema_json), value: JSON.parse(value_json) }, expected_version: selected.version });
      setSelected(null); await data.reload(); void message.success('配置新版本已保存');
    } catch (error) { void message.error(error instanceof Error ? error.message : String(error)); } finally { setBusy(false); }
  };
  return <><div className="page-heading"><div><h1>配置中心</h1><p>登记程序参数与凭据，查看配置版本及各节点的应用结果。</p></div><Space><Button onClick={() => void data.reload()}>更新状态</Button><Button type="primary" onClick={() => open()}>登记参数或凭据</Button></Space></div>{data.error && <Alert type="error" showIcon title="本次更新未完成" description={data.error} />}<Card><Table rowKey="id" loading={data.loading} dataSource={data.data ?? []} scroll={{ x: 850 }} expandable={{ expandedRowRender: parameter => <Table rowKey="node" size="small" pagination={false} dataSource={Object.entries(parameter.applications ?? {}).map(([node, application]) => ({ node, ...application, effective: parameter.effective[node] }))} columns={[{ title: '程序节点', dataIndex: 'node' }, { title: '处理版本', dataIndex: 'version' }, { title: '实际生效版本', dataIndex: 'effective' }, { title: '状态', dataIndex: 'state', render: value => states[value] || value }, { title: '原因', dataIndex: 'reason' }, { title: '确认时间', dataIndex: 'at_ms', render: timestamp }]} /> }} columns={[
    { title: '参数', render: (_, parameter) => <div>{parameter.description}<small className="table-subtext">{parameter.id}</small></div> },
    { title: '所属程序', dataIndex: 'program' }, { title: '当前值', render: (_, parameter) => parameter.secret ? <Tag>已加密保存</Tag> : <code>{typeof parameter.value === 'object' ? json(parameter.value) : String(parameter.value)}</code> },
    { title: '生效方式', dataIndex: 'dynamic', render: value => value ? '动态更新' : '重启生效' }, { title: '状态', dataIndex: 'state', render: value => <Tag color={value === 'failed' ? 'error' : value === 'applied' ? 'success' : 'warning'}>{states[value] || value}</Tag> },
    { title: '版本', dataIndex: 'version' }, { title: '操作', render: (_, parameter) => <Button type="link" onClick={() => open(parameter)}>编辑</Button> },
  ]} /></Card><Drawer title={selected?.version ? '更新程序参数' : '登记程序参数或凭据'} open={!!selected} width={Math.min(650, window.innerWidth)} onClose={() => setSelected(null)} extra={<Button type="primary" loading={busy} onClick={() => void save()}>保存新版本</Button>}><Form form={form} layout="vertical"><Form.Item name="id" label="参数标识" rules={[{ required: true }]}><Input disabled={!!selected?.version} /></Form.Item><Form.Item name="description" label="用途说明" rules={[{ required: true }]}><Input /></Form.Item><Space align="start"><Form.Item name="program" label="所属程序" rules={[{ required: true }]}><Input /></Form.Item><Form.Item name="category" label="参数分类"><Input /></Form.Item></Space><Form.Item name="dynamic" label="运行期间动态更新" valuePropName="checked"><Switch /></Form.Item><Form.Item name="secret" label="加密保存并在页面隐藏值" valuePropName="checked"><Switch /></Form.Item><Form.Item name="value_json" label="参数 JSON 值" rules={[{ required: true }]}><Input.TextArea rows={7} className="code-input" placeholder={selected?.secret ? '输入完整的新凭据内容' : ''} /></Form.Item><Form.Item name="schema_json" label="参数 Schema" rules={[{ required: true }]}><Input.TextArea rows={10} className="code-input" /></Form.Item></Form></Drawer></>;
}
