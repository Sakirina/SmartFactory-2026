import { useEffect, useState } from 'react';
import { useMutation } from '@tanstack/react-query';
import { Alert, Button, Card, Checkbox, Descriptions, Form, Input, Select, Space, Table } from 'antd';
import { json, RequestError, useResource } from './api';
import { callOperation, type Contract } from './generated-client';
import { useRetainedEdit } from './editor-state';
import { MetadataFields, MetadataValidationScope, metadataIssues } from './metadata-form';
import { ManifestDetails } from './releases';
import type { User } from './types';
import type { FieldMetadata } from './metadata-values';

// Version-two additions follow the current formal five-scenario contract.
export function templateVersionFields(template: Contract<'Template'>, version: number): FieldMetadata[] {
  const fields: FieldMetadata[] = [...template.parameters ?? []];
  const integer = (key: string, label: string, value: number, minimum: number, maximum: number) => fields.push({ key, label, type: 'integer', unit: 'ms', default: value, minimum, maximum });
  if (version >= 2) {
    if (template.required_actions?.length) integer('freshness_ms', '控制输入和互锁允许的数据年龄', 5000, 100, 60000);
    if (template.id === 'climate-ventilation') integer('average_window_ms', '温湿度平均窗口', 60000, 1000, 86400000);
    if (template.id === 'goods-counting') integer('count_window_ms', '计数窗口，0 表示累计全部有效观测', 0, 0, 86400000);
  }
  return fields;
}
type EvolutionEdit = { source: string; version: Contract<'TemplateBatch'>['version']; id: string; release: string; name: string; program: string; targets: Contract<'TemplateEvolutionTarget'>[]; configurations: Contract<'ConfigurationReference'>[] };
const empty: EvolutionEdit = { source: '', version: 0, id: '', release: '', name: '', program: '', targets: [], configurations: [] };
export function TemplateEvolution({ user, mode, currentBatch = '' }: { user: User; mode: string; currentBatch?: string }) {
  const mayEvolve = mode === 'cloud' && (user.roles.includes('admin') || user.roles.includes('engineer'));
  const catalog = useResource<Contract<'Template'>[]>('/scene-templates');
  const artifacts = useResource<Contract<'ReleaseArtifact'>[]>(mayEvolve ? '/release-artifacts' : null);
  const releases = useResource<Contract<'Release'>[]>('/releases');
  const [sourceInput, setSourceInput] = useState(currentBatch);
  const [source, setSource] = useState(currentBatch);
  const batch = useResource<Contract<'TemplateBatch'>>(source ? '/template-batches/' + encodeURIComponent(source) : null);
  const history = useResource<Contract<'TemplateEvolution'>[]>(source ? '/template-batches/' + encodeURIComponent(source) + '/evolutions' : null);
  const [edit, setEdit] = useRetainedEdit<EvolutionEdit>('template-evolution', empty);
  const [validity, setValidity] = useState<string[]>([]);
  const [result, setResult] = useState<Contract<'TemplateEvolution'>>();
  useEffect(() => { if (currentBatch) { setSourceInput(currentBatch); setSource(currentBatch); } }, [currentBatch]);
  useEffect(() => {
    if (!batch.data || edit.source === batch.data.id) return;
    setEdit({ ...empty, source: batch.data.id ?? '', version: batch.data.version, id: 'evolution-' + crypto.randomUUID(), release: 'template-release-' + crypto.randomUUID(), name: '场景模板版本演进', targets: [] });
  }, [batch.data, edit.source]);
  const evolve = useMutation({ mutationFn: async () => {
    if (!batch.data || batch.data.id !== edit.source || !edit.version || !edit.program || !edit.targets.length || !edit.id.trim() || !edit.release.trim() || !edit.name.trim()) throw new Error('请读取原批次，选择程序工件与待演进实例，并填写发布身份及名称');
    if (validity.length) throw new Error(validity.join('；'));
    for (const target of edit.targets) {
      const instance = batch.data.input?.instances?.find(item => item.id === target.instance_id);
      const template = catalog.data?.find(item => item.id === instance?.template_id);
      if (!template || !instance || Number(target.template_version) <= Number(instance.template_version)) throw new Error('目标模板版本需要高于原实例版本');
      const issues = metadataIssues(templateVersionFields(template, Number(target.template_version)), target.parameters ?? {});
      if (issues.length) throw new Error(issues.join('；'));
    }
    const saved = await callOperation('post_template_batches_id_evolve', { parameters: { path: { id: edit.source } }, body: { id: edit.id, request_id: crypto.randomUUID(), expected_version: edit.version ?? 0, release_id: edit.release, name: edit.name, program_sha256: edit.program, targets: edit.targets, configurations: edit.configurations } }, { exact: true });
    setResult(saved); await Promise.all([history.reload(), releases.reload()]); return saved;
  } });
  const references = [...new Map((releases.data ?? []).flatMap(release => release.manifest?.components?.flatMap(component => component.configuration ? [component.configuration] : []) ?? []).map(reference => [reference.kind + ':' + reference.id + ':' + reference.version + ':' + reference.digest, reference])).values()];
  const setTarget = (id: string, patch: Partial<Contract<'TemplateEvolutionTarget'>>) => setEdit({ ...edit, targets: edit.targets.map(target => target.instance_id === id ? { ...target, ...patch } : target) });
  return <Card title="已有实例的模板版本演进" className="section-card" data-testid="template-evolution">
    <Space wrap className="query-toolbar"><Input aria-label="原模板批次身份" placeholder="输入已有模板批次身份" value={sourceInput} onChange={event => setSourceInput(event.target.value)} /><Button onClick={() => { setSource(sourceInput.trim()); evolve.reset(); setResult(undefined); }}>读取原实例与配置绑定</Button><Select aria-label="选择已发布模板来源" placeholder="从已有模板发布选择原批次" showSearch optionFilterProp="label" onChange={value => { setSourceInput(value); setSource(value); evolve.reset(); setResult(undefined); }} options={[...new Map((releases.data ?? []).filter(release => release.manifest?.template?.batch_id).map(release => [release.manifest!.template!.batch_id!, { value: release.manifest!.template!.batch_id!, label: `${release.manifest?.name} / ${release.manifest?.template?.batch_id}` }])).values()]} /></Space>
    {(batch.error || history.error) && <Alert type="error" title="模板来源未读取" description={batch.error || history.error} />}
    {batch.data && <><Descriptions className="section-card" column={{ xs: 1, md: 2 }} items={[{ key: 'source', label: '原批次身份', children: batch.data.id }, { key: 'version', label: '已阅原批次版本', children: String(edit.version) }, { key: 'status', label: '原批次状态', children: batch.data.status }, { key: 'instances', label: '保留原实例及配置绑定', children: `${batch.data.input?.instances?.length ?? 0} 个实例 / ${batch.data.bindings?.length ?? 0} 项绑定` }]} />
      <Form layout="vertical"><div className="template-bindings"><Form.Item label="模板演进身份" required><Input aria-label="模板演进身份" value={edit.id} onChange={event => setEdit({ ...edit, id: event.target.value })} /></Form.Item><Form.Item label="模板固定发布身份" required><Input aria-label="模板固定发布身份" value={edit.release} onChange={event => setEdit({ ...edit, release: event.target.value })} /></Form.Item><Form.Item label="模板固定发布名称" required><Input aria-label="模板固定发布名称" value={edit.name} onChange={event => setEdit({ ...edit, name: event.target.value })} /></Form.Item><Form.Item label="模板发布程序工件" required><Select aria-label="模板发布程序工件" value={edit.program || undefined} showSearch optionFilterProp="label" onChange={value => setEdit({ ...edit, program: value })} options={(artifacts.data ?? []).map(item => ({ value: item.sha256, label: `${item.build?.version} / ${item.build?.goos} / ${item.build?.goarch} / 数据库 ${String(item.build?.migration_minimum)} 至 ${String(item.build?.migration_maximum)}` }))} /></Form.Item></div>
      <Form.Item label="附加固定参数引用"><Select aria-label="附加固定参数引用" mode="multiple" value={edit.configurations.map(reference => reference.kind + ':' + reference.id + ':' + reference.version + ':' + reference.digest)} onChange={values => setEdit({ ...edit, configurations: references.filter(reference => values.includes(reference.kind + ':' + reference.id + ':' + reference.version + ':' + reference.digest)) })} options={references.filter(reference => reference.kind === 'parameter').map(reference => ({ value: reference.kind + ':' + reference.id + ':' + reference.version + ':' + reference.digest, label: `${reference.id} / v${String(reference.version)} / ${reference.digest}` }))} /></Form.Item></Form>
      <MetadataValidationScope onChange={setValidity} key={source}>{batch.data.input?.instances?.map(instance => {
        const target = edit.targets.find(item => item.instance_id === instance.id);
        const template = catalog.data?.find(item => item.id === instance.template_id);
        const fields = template ? templateVersionFields(template, Number(target?.template_version ?? instance.template_version)) : [];
        return <Card key={instance.id} title={instance.name} className="section-card" extra={<Checkbox aria-label={'演进实例 ' + instance.id} checked={!!target} disabled={!template?.available_versions?.some(version => Number(version) > Number(instance.template_version))} onChange={event => {
          if (!event.target.checked) setEdit({ ...edit, targets: edit.targets.filter(item => item.instance_id !== instance.id) });
          else { const version = Number(template?.available_versions?.find(value => Number(value) > Number(instance.template_version)) ?? 2); const nextFields = template ? templateVersionFields(template, version) : []; setEdit({ ...edit, targets: [...edit.targets, { instance_id: instance.id, template_version: version, parameters: { ...Object.fromEntries(nextFields.map(field => [field.key ?? '', field.default])), ...instance.parameters } }] }); }
        }}>选择此实例</Checkbox>}><Descriptions column={{ xs: 1, md: 2 }} items={[{ key: 'template', label: '原模板与版本', children: `${template?.name || instance.template_id} / v${String(instance.template_version)}` }, { key: 'device', label: '保留设备与版本', children: `${instance.device_id} / v${String(instance.device_version)}` }, { key: 'config', label: '保留正式配置与版本', children: `${instance.configuration_id} / v${String(instance.configuration_version)}` }, { key: 'identity', label: '保留实例身份', children: instance.id }]} />{target && <><Form layout="vertical"><Form.Item label="目标模板版本"><Select aria-label={'目标模板版本 ' + instance.id} value={String(target.template_version)} onChange={value => setTarget(instance.id, { template_version: Number(value) })} options={(template?.available_versions ?? []).filter(version => Number(version) > Number(instance.template_version)).map(version => ({ value: String(version), label: 'v' + String(version) }))} /></Form.Item></Form><MetadataFields namespace={'evolution:' + instance.id} prefix={instance.name + '：'} fields={fields} value={target.parameters ?? {}} onChange={parameters => setTarget(instance.id, { parameters })} /></>}</Card>;
      })}</MetadataValidationScope>
      <Button className="section-card" type="primary" disabled={!mayEvolve || batch.data.status !== 'prepared' || !edit.targets.length} loading={evolve.isPending} onClick={() => evolve.mutate()}>检查参数并生成模板固定发布</Button>
      {evolve.error && <Alert type="error" className="section-card" title="模板演进未完成，当前参数继续保留" description={evolve.error.message} />}{evolve.error instanceof RequestError && evolve.error.status === 409 && <Button onClick={async () => { const current = await batch.reload(); const value = current.data as Contract<'TemplateBatch'> | undefined; if (value) setEdit(previous => previous.source === value.id ? { ...previous, version: value.version } : previous); evolve.reset(); }}>核对原批次并更新已阅版本</Button>}
    </>}
    {result && <Card title="模板演进影响与固定发布" className="section-card"><Descriptions column={1} items={[{ key: 'devices', label: '影响设备', children: result.device_ids?.join('、') }, { key: 'nodes', label: '影响节点', children: result.node_ids?.join('、') }, { key: 'rules', label: '沿用原规则身份', children: result.definition_ids?.join('、') }, { key: 'source', label: '原批次与版本', children: `${result.source_batch_id} / v${String(result.source_batch_version)}` }]} /><ManifestDetails manifest={result.release?.manifest} digest={result.release?.sha256} order={result.release?.order} /><a href={'#releases?release=' + encodeURIComponent(result.release?.id ?? '')}>为此模板发布安排节点批次</a></Card>}
    <Table className="section-card" rowKey="id" dataSource={history.data ?? []} scroll={{ x: 900 }} columns={[{ title: '演进记录', dataIndex: 'id' }, { title: '原批次版本', render: (_, item) => String(item.source_batch_version) }, { title: '实例 / 新版本', render: (_, item) => item.instances?.map(instance => `${instance.id} / v${String(instance.template_version)}`).join('；') }, { title: '影响节点', render: (_, item) => item.node_ids?.join('、') }, { title: '固定发布', render: (_, item) => <a href={'#releases?release=' + encodeURIComponent(item.release?.id ?? '')}>{item.release?.id}</a> }]} />
  </Card>;
}
