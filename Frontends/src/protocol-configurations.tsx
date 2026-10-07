import { useEffect, useState } from 'react';
import { useMutation } from '@tanstack/react-query';
import { Alert, Button, Card, Descriptions, Drawer, Input, Select, Space, Table, Tabs, Tag } from 'antd';
import { json, useResource } from './api';
import { callOperation, type Contract } from './generated-client';
import { parseExactJSON } from './precision';
import { PageTitle, ErrorNotice, StateTag } from './views';
import { MetadataValidationScope, ProtocolParameterForm, type Parameters } from './metadata-form';
import type { Entity } from './types';

export function ProtocolConfigurations({ mode, initialID = '' }: { mode: string; initialID?: string }) {
  const protocols = useResource<Contract<'ProtocolMetadata'>[]>('/device-protocols'); const devices = useResource<Entity[]>('/entities');
  const connectors = useResource<Contract<'ConnectorConfigurationDetail'>[]>('/connector-configurations', 5000);
  const [deviceID, setDeviceID] = useState(initialID); const detail = useResource<Contract<'DeviceConfigurationDetail'>>(deviceID ? '/devices/' + encodeURIComponent(deviceID) + '/configuration' : null);
  const [editing, setEditing] = useState<{ kind: 'device' | 'connector'; protocol: string; id: string; version: unknown; allowed: boolean; group: string; edge: string }>();
  const [validationReset,setValidationReset] = useState(0); const [fieldErrors,setFieldErrors]=useState<string[]>([]); const [parameters, setParameters] = useState<Parameters>({}); const [raw, setRaw] = useState('{}'); const [rawError, setRawError] = useState(''); const [validation, setValidation] = useState<Contract<'Result'>>();
  const set = (value: Parameters,resetErrors=false) => { setParameters(value); setRaw(json(value)); setRawError(''); setValidation(undefined); if(resetErrors)setValidationReset(key=>key+1); };
  const openDevice = () => { if (!detail.data?.entity) return; const entity = detail.data.entity; set(detail.data.configuration?.parameters as Parameters ?? {},true); setEditing({ kind: 'device', protocol: detail.data.configuration?.protocol ?? entity.protocol ?? '', id: entity.id ?? '', version: entity.version, allowed: !!detail.data.allowed_actions?.find(action => action.action === 'save' && action.allowed), group: entity.parent_id ?? 'factory', edge: entity.edge_id ?? '' }); };
  const openConnector = async (item: Contract<'ConnectorConfigurationDetail'>) => {
    const connector = item.configuration!;
    const result = await callOperation('post_device_configurations_validate', { body: { protocol: connector.protocol ?? '', config: connector.config } }, { exact: true });
    set(result.parameters as Parameters ?? {},true); setEditing({ kind: 'connector', protocol: connector.protocol ?? '', id: connector.id ?? '', version: connector.version, allowed: !!item.allowed_actions?.find(action => action.action === 'save' && action.allowed), group: connector.group_id ?? 'factory', edge: connector.edge_id ?? '' });
  };
  useEffect(() => { if (initialID) setDeviceID(initialID); }, [initialID]);
  const validate = useMutation({ mutationFn: async () => { if (!editing) return; if (rawError || fieldErrors.length) throw new Error(rawError || fieldErrors.join('；')); const result = await callOperation('post_device_configurations_validate', { body: { protocol: editing.protocol, parameters: parameters as Contract<'Parameters'> } }, { exact: true }); setValidation(result); return result; } });
  const save = useMutation({ mutationFn: async () => {
    const result = await validate.mutateAsync(); if (!result?.valid || !editing) throw new Error('完成配置预检后保存参数');
    if (!editing.allowed) throw new Error('当前服务和身份未允许保存此配置');
    if (editing.kind === 'device') await callOperation('post_device_configurations', { body: { id: editing.id, request_id: crypto.randomUUID(), expected_version: editing.version as never, parameters: parameters as Contract<'Parameters'> } }, { exact: true });
    else await callOperation('post_connector_configurations', { body: { request_id: crypto.randomUUID(), expected_version: editing.version as never, group_id: editing.group, edge_id: editing.edge, protocol: editing.protocol, parameters: parameters as Contract<'Parameters'> } }, { exact: true });
    setEditing(undefined); await detail.reload(); await connectors.reload();
  }, onError: () => { void detail.reload(); void connectors.reload(); } });
  const metadata = protocols.data?.find(protocol => protocol.protocol === editing?.protocol || protocol.aliases?.includes(editing?.protocol ?? ''));
  return <><PageTitle title="协议与设备参数" note="依据协议目录编辑连接、采样、数据点和动作映射，并核对正式配置版本与下发回执。" /><ErrorNotice error={protocols.error || devices.error || connectors.error || detail.error} retry={() => { void protocols.reload(); void connectors.reload(); void detail.reload(); }} />
    <Card title="设备基础参数"><Space wrap><Select aria-label="参数设备" showSearch optionFilterProp="label" value={deviceID || undefined} placeholder="选择设备" onChange={setDeviceID} style={{ minWidth: 280 }} options={(devices.data ?? []).filter(device => device.kind === 'device').map(device => ({ value: device.id, label: device.name }))} /><Button disabled={!detail.data} onClick={openDevice}>编辑设备参数</Button></Space>{detail.data && <Descriptions className="section-card" column={2} items={[{ key: 'device', label: '设备', children: detail.data.entity?.name }, { key: 'version', label: '已阅版本', children: String(detail.data.entity?.version) }, { key: 'protocol', label: '协议', children: detail.data.configuration?.protocol }, { key: 'permission', label: '操作条件', children: detail.data.allowed_actions?.map(action => action.reason || action.action).join('；') }]} />}</Card>
    <Card title="正式连接器配置与下发状态" className="section-card"><Table rowKey={item => item.configuration?.id ?? ''} dataSource={connectors.data ?? []} scroll={{ x: 900 }} columns={[{ title: '配置身份', render: (_, item) => item.configuration?.id }, { title: '协议', render: (_, item) => item.configuration?.protocol }, { title: '配置版本', render: (_, item) => String(item.configuration?.version) }, { title: '归属节点', render: (_, item) => item.configuration?.edge_id }, { title: '已确认版本', render: (_, item) => String(item.receipt?.configuration_version ?? 0) }, { title: '下发状态', render: (_, item) => <><StateTag status={item.receipt?.status ?? 'pending'} />{item.receipt?.reason}</> }, { title: '操作', render: (_, item) => <Button onClick={() => void openConnector(item).catch(error => { setRawError(String(error)); })}>查看连接器参数</Button> }]} /></Card>
    <Drawer title={editing?.kind === 'device' ? '设备基础参数编辑' : '连接器参数编辑'} open={!!editing} onClose={() => setEditing(undefined)} width="min(920px, 100vw)" extra={<Space><Button loading={validate.isPending} onClick={() => validate.mutate()}>配置预检</Button><Button type="primary" disabled={!editing?.allowed} loading={save.isPending} onClick={() => save.mutate()}>保存参数</Button></Space>}>
      <Space wrap><Tag>{metadata?.label || editing?.protocol}</Tag><span>已阅版本 {String(editing?.version)}</span><span>服务模式 {mode}</span></Space>
      {!editing?.allowed && <Alert type="info" title="当前配置允许读取" description="保存操作由归属节点及当前授权决定，请使用归属节点的现场工作台。" />}
      {(rawError || validate.error || save.error) && <Alert type="error" title="参数操作未完成" description={rawError || validate.error?.message || save.error?.message} />}
      {save.error && <Button onClick={async () => {
        if (!editing) return;
        if (editing.kind === 'device') { const current = await detail.reload(); const value = current.data as Contract<'DeviceConfigurationDetail'> | undefined; if (value?.entity) setEditing({ ...editing, version:value.entity.version, allowed:!!value.allowed_actions?.some(action=>action.action==='save'&&action.allowed) }); }
        else { const current = await connectors.reload(); const value = (current.data as Contract<'ConnectorConfigurationDetail'>[] | undefined)?.find(item=>item.configuration?.id===editing.id); if(value?.configuration) setEditing({...editing,version:value.configuration.version,allowed:!!value.allowed_actions?.some(action=>action.action==='save'&&action.allowed)}); }
        save.reset();
      }}>采用更新后的保存依据，保留已填写参数</Button>}
      {validation && <Alert type={validation.valid ? 'success' : 'error'} title={validation.valid ? '配置预检通过' : '配置预检存在问题'} description={validation.issues?.map(issue => `${issue.path}：${issue.message}`).join('；')} />}
      <MetadataValidationScope key={editing?.id} resetKey={validationReset} onChange={setFieldErrors}><Tabs items={[{ key: 'metadata', label: '元数据表单', children: metadata && <ProtocolParameterForm metadata={metadata} value={parameters} onChange={set} /> }, { key: 'raw', label: '完整参数与扩展', children: <><p>未修改的扩展字段与数据点扩展随同本次配置保存，数值使用精确 JSON 输入。</p><Input.TextArea aria-label="完整协议参数" rows={25} value={raw} onChange={event => { setRaw(event.target.value); try { const value = parseExactJSON(event.target.value); if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('参数需要 JSON 对象'); setParameters(value as Parameters); setRawError(''); setValidation(undefined); setValidationReset(key=>key+1); } catch (error) { setRawError(String(error)); } }} /></> }]} />
      </MetadataValidationScope>
    </Drawer></>;
}
