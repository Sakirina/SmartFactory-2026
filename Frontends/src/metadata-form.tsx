import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import { Alert, Button, Form, Input, Select, Space, Tabs, Tag } from 'antd';
import { json } from './api';
import type { Contract } from './generated-client';
import { parseExactJSON, stringifyExactJSON } from './precision';

export type FieldMetadata = Contract<'ParameterMetadata'> & { sensitive?: boolean };
export type Parameters = Record<string, unknown>;
const FieldValidity = createContext<{report:(id:string,error:string)=>void;revision:number}>({report:()=>{},revision:0});
export function MetadataValidationScope({children,onChange,resetKey=0}:{children:ReactNode;onChange:(errors:string[])=>void;resetKey?:number}) {
  const [errors,setErrors] = useState<Record<string,string>>({}); const errorKey = JSON.stringify(errors);
  const report = useCallback((id:string,error:string)=>setErrors(old=>old[id]===error?old:{...old,[id]:error}),[]);
  useEffect(()=>setErrors({}),[resetKey]);
  useEffect(()=>onChange(Object.values(errors).filter(Boolean)),[errorKey]);
  const value = useMemo(()=>({report,revision:resetKey}),[report,resetKey]);
  return <FieldValidity.Provider value={value}>{children}</FieldValidity.Provider>;
}
export { parseField, metadataIssues } from './metadata-values';
import { parseField, metadataIssues } from './metadata-values';
export function MetadataFields({ fields, value, onChange, prefix = '', namespace = '' }: { fields: FieldMetadata[]; value: Parameters; onChange: (value: Parameters) => void; prefix?: string;namespace?:string }) {
  const set = (key: string, item: unknown) => { const next = { ...value }; if (item === undefined) delete next[key]; else next[key] = item; onChange(next); };
  return <Form component={false} layout="vertical" className="metadata-fields">{fields.map(field => <MetadataField key={field.key} field={field} value={value[field.key ?? '']} onChange={item => set(field.key ?? '', item)} prefix={prefix} namespace={namespace} />)}</Form>;
}
function MetadataField({ field, value, onChange, prefix, namespace }: { field: FieldMetadata; value: unknown; onChange: (value: unknown) => void; prefix: string;namespace:string }) {
  const validity = useContext(FieldValidity);
  const id = namespace + ':' + field.key;
  const display = (item: unknown) => item === undefined ? '' : field.type === 'string' ? String(item) : stringifyExactJSON(item);
  const [text, setText] = useState(() => display(value)); const [error, setError] = useState('');
  useEffect(() => { setText(display(value)); setError(''); validity.report(id,''); }, [value,validity.revision,id]);
  useEffect(() => () => validity.report(id,''), [id,validity.report]);
  const label = `${prefix}${field.label || field.key}${field.unit ? '（' + field.unit + '）' : ''}`;
  const update = (text: string) => { setText(text); try { const item = parseField(field, text); onChange(item); setError(''); validity.report(id,''); } catch (error) { const message = error instanceof Error ? error.message : String(error); setError(message); validity.report(id,message); } };
  return <Form.Item label={label} required={field.required} help={error || field.description} validateStatus={error ? 'error' : undefined}>
    {field.enum?.length ? <Select aria-label={label} allowClear={!field.required} value={value === undefined ? undefined : String(value)} onChange={item => update(item ?? '')} options={field.enum.map(item => ({ value: item, label: item }))} />
      : field.type === 'boolean' ? <Select aria-label={label} allowClear value={value === undefined ? undefined : String(value)} onChange={item => update(item ?? '')} options={[{ value: 'true', label: '启用' }, { value: 'false', label: '停用' }]} />
      : field.type === 'json' ? <Input.TextArea aria-label={label} rows={3} value={text} onChange={event => update(event.target.value)} />
      : field.sensitive ? <Input.Password aria-label={label} value={text} onChange={event => update(event.target.value)} autoComplete="new-password" />
      : <Input aria-label={label} value={text} onChange={event => update(event.target.value)} inputMode={['integer', 'number'].includes(field.type ?? '') ? 'decimal' : 'text'} />}
    {(field.minimum !== undefined || field.maximum !== undefined) && <small className="muted">允许范围 {String(field.minimum ?? '不限')} 至 {String(field.maximum ?? '不限')}</small>}
  </Form.Item>;
}
export function NodeParameterForm({ metadata, parameters, onApply }: { metadata: Contract<'NodeMetadata'>; parameters: Parameters; onApply: (parameters: Parameters) => void }) {
  const [resetKey,setResetKey] = useState(0); const [fieldErrors,setFieldErrors] = useState<string[]>([]); const [value, setValue] = useState(parameters); const [error, setError] = useState(''); const [rawError, setRawError] = useState(''); const [text, setText] = useState(json(parameters));
  useEffect(() => { setValue(parameters); setText(json(parameters)); setError(''); setRawError(''); setResetKey(key=>key+1); }, [parameters]);
  const apply = () => { if (rawError || fieldErrors.length) {setError(rawError || fieldErrors.join('；'));return;} const issues = metadataIssues(metadata.parameters ?? [], value); if (issues.length) setError(issues.join('；')); else { setError(''); onApply(value); } };
  return <MetadataValidationScope resetKey={resetKey} onChange={setFieldErrors}><p>{metadata.description}</p><Space wrap>{(metadata.inputs ?? []).map(port => <Tag key={'in' + port.name}>输入 {port.name} / {port.type}</Tag>)}{(metadata.outputs ?? []).map(port => <Tag key={'out' + port.name}>输出 {port.name} / {port.type}</Tag>)}</Space><Tabs items={[{ key: 'fields', label: '基础参数', children: <MetadataFields namespace={'node:'+metadata.type} fields={metadata.parameters ?? []} value={value} onChange={next => { setValue(next); setText(json(next)); setRawError(''); }} /> }, { key: 'extensions', label: '完整参数与扩展', children: <Input.TextArea aria-label="节点完整参数" rows={10} value={text} onChange={event => { setText(event.target.value); try { const next = parseExactJSON(event.target.value); if (!next || typeof next !== 'object' || Array.isArray(next)) throw new Error('参数需要 JSON 对象'); setValue(next as Parameters); setError(''); setRawError(''); setResetKey(key=>key+1); } catch (error) { setRawError(String(error)); } }} /> }]} />{(error || rawError) && <Alert type="error" title="节点参数需要调整" description={rawError || error} />}<Button type="primary" disabled={!!fieldErrors.length || !!rawError} onClick={apply}>应用基础参数</Button></MetadataValidationScope>;
}
function getPath(value: Parameters, path: string): unknown { return path.split('.').reduce<unknown>((item, key) => item && typeof item === 'object' ? (item as Parameters)[key] : undefined, value); }
function setPath(value: Parameters, path: string, item: unknown): Parameters { const next = structuredClone(value); const keys = path.split('.'); let parent = next; keys.slice(0, -1).forEach(key => { if (!parent[key] || typeof parent[key] !== 'object') parent[key] = {}; parent = parent[key] as Parameters; }); parent[keys.at(-1)!] = item; return next; }
export function ProtocolParameterForm({ metadata, value, onChange }: { metadata: Contract<'ProtocolMetadata'>; value: Parameters; onChange: (value: Parameters,resetErrors?:boolean) => void }) {
  return <Tabs className="protocol-tabs" items={(metadata.sections ?? []).map(section => {
    const path = section.path ?? ''; const item = getPath(value, path);
    const namespace = metadata.protocol + ':' + path;
    const edit = (next: unknown,resetErrors=false) => onChange(setPath(value, path, next),resetErrors);
    if (section.repeated) { const items = Array.isArray(item) ? item as Parameters[] : []; return { key: path, label: path === 'datapoints' ? '数据点' : path, children: <>{items.map((point, index) => <div className="parameter-block" key={index}><h4>数据点 {index + 1}</h4><MetadataFields namespace={namespace+':'+index} prefix={`数据点 ${index + 1}：`} fields={section.fields ?? []} value={point} onChange={next => edit(items.map((old, i) => i === index ? next : old))} /><Button danger onClick={() => edit(items.filter((_, i) => i !== index),true)}>删除数据点</Button></div>)}<Button onClick={() => edit([...items, {}],true)}>增加数据点</Button></> }; }
    if (section.keyed) { const items = item && typeof item === 'object' ? item as Parameters : {}; return { key: path, label: '动作映射', children: <>{Object.entries(items).map(([key, point]) => <div className="parameter-block" key={key}><h4>{key}</h4><MetadataFields namespace={namespace+':'+key} prefix={key + '：'} fields={section.fields ?? []} value={point as Parameters} onChange={next => edit({ ...items, [key]: next })} /></div>)}<p>动作名称和扩展对象可在完整参数中编辑。</p></> }; }
    return { key: path, label: ({ connection: '连接', polling: '采样', address: '设备地址', report_strategy: '上报策略', 'connection.tls': 'TLS' } as Record<string, string>)[path] || path, children: <MetadataFields namespace={namespace} fields={section.fields ?? []} value={item && typeof item === 'object' ? item as Parameters : {}} onChange={edit} /> };
  })} />;
}
