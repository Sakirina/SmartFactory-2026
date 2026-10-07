import { useState } from 'react';
import { useMutation } from '@tanstack/react-query';
import { Alert, Button, Card, Descriptions, Form, Input, Select, Space, Table, Tag } from 'antd';
import { json, useResource } from './api';
import { callOperation, type Contract } from './generated-client';
import { MetadataValidationScope, MetadataFields, metadataIssues, type Parameters } from './metadata-form';
import { PageTitle, ErrorNotice } from './views';
import type { Entity, User } from './types';
import { resourceLink } from './resource-navigation';
import { parseExactJSON } from './precision';
import { TemplateEvolution } from './template-evolution';

const protocol = (value?: string) => value === 'modbus' ? 'modbus_tcp' : value === 'mqtt' ? 'mqtt_device' : value;
export function SceneTemplates({ user, mode }: { user: User; mode: string }) {
  const templates = useResource<Contract<'Template'>[]>('/scene-templates'); const entities = useResource<Entity[]>('/entities'); const configurations = useResource<Contract<'ConnectorConfigurationDetail'>[]>('/connector-configurations');
  const [chosen, setChosen] = useState(''); const template = templates.data?.find(item => item.id === chosen);
  const [deviceID, setDeviceID] = useState(''); const [configurationID, setConfigurationID] = useState(''); const [name, setName] = useState(''); const [instanceID, setInstanceID] = useState(''); const [safety, setSafety] = useState(user.id); const [parameters, setParameters] = useState<Parameters>({});
  const [fieldErrors,setFieldErrors]=useState<string[]>([]); const [instances, setInstances] = useState<Contract<'TemplateInstance'>[]>([]); const [inputError, setInputError] = useState(''); const [batchID, setBatchID] = useState('');
  const compatibleDevices = (entities.data ?? []).filter(item => {
    if (!template || item.kind !== 'device' || protocol(item.protocol) !== template.protocol) return false;
    try {
      const config = (item.config as Record<string,unknown> | undefined)?.deviceConfig as Record<string,unknown> | undefined;
      const encoded = config?.datapoints;
      const points = typeof encoded === 'string' ? parseExactJSON(new TextDecoder().decode(Uint8Array.from(atob(encoded), character => character.charCodeAt(0)))) : encoded;
      const keys = new Set(Array.isArray(points) ? points.map(point => point.key) : []);
      return (template.required_keys ?? []).every(key => keys.has(key));
    } catch { return false; }
  });
  const selectedDevice = compatibleDevices.find(item => item.id === deviceID);
  const connectorID = ((selectedDevice?.config as Record<string,unknown> | undefined)?.deviceConfig as Record<string,unknown> | undefined)?.connectorId;
  const compatibleConfigurations = (configurations.data ?? []).filter(item => item.configuration?.protocol === template?.protocol && item.configuration?.connector_id === connectorID && item.configuration?.edge_id === selectedDevice?.edge_id);
  const batch = useResource<Contract<'TemplateBatch'>>(batchID ? '/template-batches/' + encodeURIComponent(batchID) : null);
  const choose = (id: string) => { const item = templates.data?.find(value => value.id === id); setChosen(id); setDeviceID(''); setConfigurationID(''); setName((item?.name ?? '') + ' · ' + crypto.randomUUID().slice(0,8)); setInstanceID('scene-' + crypto.randomUUID().slice(0,8)); setParameters(Object.fromEntries((item?.parameters ?? []).map(field => [field.key, field.default]))); setInputError(''); };
  const add = () => {
    if (fieldErrors.length) {setInputError(fieldErrors.join('；'));return;}
    const device = entities.data?.find(item => item.id === deviceID); const connector = configurations.data?.find(item => item.configuration?.id === configurationID)?.configuration;
    if (!template || !device || !connector || !instanceID.trim() || !name.trim()) { setInputError('请选择模板、设备、正式配置，并填写实例身份与名称'); return; }
    const issues = metadataIssues(template.parameters ?? [], parameters); if (issues.length) { setInputError(issues.join('；')); return; }
    if (instances.length >= 20) { setInputError('每个批次最多包含二十个实例'); return; }
    setInstances([...instances, { template_id: template.id ?? '', template_version: template.version ?? 0, id: instanceID, name, device_id: device.id, device_version: device.version, configuration_id: connector.id ?? '', configuration_version: connector.version ?? 0, safety_user_id: safety, parameters }]);
    setInstanceID('scene-' + crypto.randomUUID().slice(0,8)); setInputError('');
  };
  const prepare = useMutation({ mutationFn: async () => { const saved = await callOperation('post_template_batches', { body: { id: 'batch-' + crypto.randomUUID(), request_id: crypto.randomUUID(), group_id: 'factory', instances } }, { exact: true }); setBatchID(saved.id ?? ''); return saved; } });
  const retry = useMutation({ mutationFn: async () => { if (!batch.data) throw new Error('请读取批次详情'); const saved = await callOperation('post_template_batches_id_retry', { parameters: { path: { id: batch.data.id ?? '' } }, body: { expected_version: batch.data.version ?? 0, request_id: crypto.randomUUID() } }, { exact: true }); await batch.reload(); return saved; } });
  return <><PageTitle title="场景模板" note="选择五类场景、绑定已阅设备和正式连接器配置，在同一批次准备可评审的规则草稿。" /><ErrorNotice error={templates.error || entities.error || configurations.error || batch.error} retry={() => { void templates.reload(); void entities.reload(); void configurations.reload(); void batch.reload(); }} />
    <div className="definition-grid">{templates.data?.map(item => <Card key={item.id} title={item.name} extra={<Tag>v{String(item.version)}</Tag>}><p>{item.protocol}，生成 {String(item.definition_count)} 份草稿</p><p>需要数据字段：{item.required_keys?.join('、')}</p><p>需要设备动作：{item.required_actions?.join('、') || '此模板包含数据计算'}</p><Button onClick={() => choose(item.id ?? '')}>使用此模板</Button></Card>)}</div>
    {template && <Card title={'准备模板实例：' + template.name} className="section-card"><Form layout="vertical"><div className="template-bindings"><Form.Item label="实例身份" required><Input aria-label="实例身份" value={instanceID} onChange={event => setInstanceID(event.target.value)} /></Form.Item><Form.Item label="实例名称" required><Input aria-label="实例名称" value={name} onChange={event => setName(event.target.value)} /></Form.Item><Form.Item label="绑定设备与版本" required><Select aria-label="模板绑定设备" value={deviceID || undefined} showSearch optionFilterProp="label" onChange={value => { setDeviceID(value); setConfigurationID(''); }} options={compatibleDevices.map(item => ({ value: item.id, label: `${item.name} / v${item.version}` }))} /></Form.Item><Form.Item label="正式配置与版本" required><Select aria-label="模板绑定配置" value={configurationID || undefined} onChange={setConfigurationID} showSearch optionFilterProp="label" options={compatibleConfigurations.map(item => ({ value: item.configuration?.id, label: `${item.configuration?.id} / v${String(item.configuration?.version)} / ${item.receipt?.status}` }))} /></Form.Item><Form.Item label="安全成员身份"><Input aria-label="安全成员身份" value={safety} onChange={event => setSafety(event.target.value)} /></Form.Item></div></Form><MetadataValidationScope key={chosen} onChange={setFieldErrors}><MetadataFields fields={template.parameters ?? []} value={parameters} onChange={setParameters} /></MetadataValidationScope><Button type="primary" onClick={add}>加入待准备批次</Button>{inputError && <Alert type="error" title="实例输入需要调整" description={inputError} />}</Card>}
    <Card title="待准备批次" className="section-card"><Table rowKey={item => item.id} dataSource={instances} scroll={{ x: 900 }} columns={[{ title: '实例', dataIndex: 'name' }, { title: '模板', dataIndex: 'template_id' }, { title: '设备 / 版本', render: (_, item) => `${item.device_id} / ${String(item.device_version)}` }, { title: '配置 / 版本', render: (_, item) => `${item.configuration_id} / ${String(item.configuration_version)}` }, { title: '操作', render: (_, item) => <Button onClick={() => setInstances(instances.filter(value => value !== item))}>移除此实例</Button> }]} /><Button type="primary" disabled={!instances.length || mode !== 'cloud'} loading={prepare.isPending} onClick={() => prepare.mutate()}>检查冲突并批量准备草稿</Button>{prepare.error && <Alert type="error" title="批次准备未完成" description={prepare.error.message} />}</Card>
    {batch.data && <Card title="批次保存结果" className="section-card"><Descriptions column={2} items={[{ key: 'id', label: '批次身份', children: batch.data.id }, { key: 'version', label: '版本与状态', children: `${String(batch.data.version)} / ${batch.data.status}` }, { key: 'count', label: '生成草稿', children: String(batch.data.drafts?.length ?? 0) }, { key: 'attempts', label: '尝试次数', children: String(batch.data.attempts?.length ?? 0) }]} />{batch.data.status === 'failed' && <><Alert type="error" title="此批次保存了失败结果" description={batch.data.failures?.map(failure => `${failure.instance_id}：${failure.message}`).join('；')} /><Button loading={retry.isPending} onClick={() => retry.mutate()}>外部条件修复后重试原批次</Button></>}{retry.error && <Alert type="error" title="批次重试未完成" description={retry.error.message} />}<Table size="small" rowKey="instance_id" dataSource={batch.data.bindings ?? []} columns={[{ title: '实例', dataIndex: 'instance_id' }, { title: '配置 / 版本', render: (_, item) => `${item.configuration_id} / ${String(item.configuration_version)}` }, { title: '已确认版本', render: (_, item) => String(item.applied_version) }, { title: '下发状态', dataIndex: 'application_status' }]} /><Table size="small" rowKey="id" dataSource={batch.data.drafts ?? []} columns={[{ title: '草稿', render: (_, item) => <a href={resourceLink('draft', item.id)}>{item.definition?.name}</a> }, { title: '类型', render: (_, item) => item.definition?.kind }, { title: '修订', render: (_, item) => String(item.version) }]} /><details><summary>批次输入与尝试历史</summary><pre>{json({ input: batch.data.input, attempts: batch.data.attempts })}</pre></details></Card>}
    <TemplateEvolution user={user} mode={mode} currentBatch={batchID} />
  </>;
}
