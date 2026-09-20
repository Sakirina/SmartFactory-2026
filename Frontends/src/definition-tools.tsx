import { useState } from 'react';
import { Alert, App, Button, Descriptions, Input, Modal, Select, Space, Table, Tag } from 'antd';
import { api, json, post, timestamp } from './api';
import type { Definition, Draft, Json } from './types';

export function DraftSimulation({ draft, save }: { draft: Draft; save: () => Promise<Draft | null> }) {
  const [open, setOpen] = useState(false);
  const [sample, setSample] = useState('');
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<Record<string, Json>>();
  const [error, setError] = useState('');
  const run = async () => { setBusy(true); setError(''); try { const saved = await save(); if (!saved) return; setResult(await post(`/drafts/${encodeURIComponent(saved.id)}/simulate`, { point: JSON.parse(sample) })); } catch (error) { setError(error instanceof Error ? error.message : String(error)); } finally { setBusy(false); } };
  return <><Button onClick={() => { setSample(json({ id: 'simulation-' + crypto.randomUUID(), device_id: draft.definition.selector.device_ids[0] || '', key: draft.definition.selector.keys[0] || '', value: 26, quality: 'GOOD', observed_ms: Date.now(), time_source: 'simulation' })); setResult(undefined); setError(''); setOpen(true); }}>模拟运行</Button><Modal title="草稿模拟运行" open={open} onCancel={() => setOpen(false)} width={850} footer={<Space><Button onClick={() => setOpen(false)}>关闭</Button><Button type="primary" loading={busy} onClick={() => void run()}>运行模拟</Button></Space>}><p>模拟使用当前草稿、提供的输入样本与对应时间的历史数据，展示节点结果、质量统计和触发判断。</p><Input.TextArea aria-label="模拟输入样本" value={sample} onChange={event => setSample(event.target.value)} rows={10} className="code-input" />{error && <Alert className="section-card" type="error" showIcon title="模拟未完成" description={error} />}{result && <div className="section-card"><Descriptions column={2} items={[{ key: 'time', label: '输入时间', children: timestamp(Number(result.at_ms)) }, { key: 'trigger', label: '预案触发判断', children: result.trigger ? '满足触发条件' : '未满足触发条件' }]} /><h4>节点输出</h4><pre className="json-view">{json(result.values)}</pre><h4>质量统计</h4><pre className="json-view">{json(result.quality)}</pre></div>}</Modal></>;
}

export function DefinitionHistory({ definition, restore }: { definition: Definition; restore: (draft: Draft) => void }) {
  const [open, setOpen] = useState(false);
  const [versions, setVersions] = useState<Definition[]>([]);
  const [selected, setSelected] = useState<Definition>();
  const [busy, setBusy] = useState(false);
  const { message } = App.useApp();
  const load = async () => { try { const result = await api<Definition[]>(`/definitions/${encodeURIComponent(definition.id)}/versions`); setVersions(result); setSelected(result[result.length - 1]); setOpen(true); } catch (error) { void message.error(error instanceof Error ? error.message : String(error)); } };
  const create = async () => { if (!selected) return; setBusy(true); try { const draft = await post<Draft>(`/definitions/${encodeURIComponent(definition.id)}/rollback`, { version: selected.version }); restore(draft); setOpen(false); void message.success('历史版本已复制为待评审草稿'); } catch (error) { void message.error(error instanceof Error ? error.message : String(error)); } finally { setBusy(false); } };
  return <><Button onClick={() => void load()}>版本记录</Button><Modal title="版本记录与回滚评审" open={open} onCancel={() => setOpen(false)} width={1000} footer={<Space><Button onClick={() => setOpen(false)}>关闭</Button><Button type="primary" loading={busy} disabled={!selected} onClick={() => void create()}>以此版本创建回滚草稿</Button></Space>}><p>回滚草稿将绑定当前正式版本，完成差异评审后发布为新的版本。</p><Select aria-label="历史版本" value={selected?.version} onChange={version => setSelected(versions.find(item => item.version === version))} options={versions.map(item => ({ value: item.version, label: `v${item.version} · ${timestamp(item.effective_ms)}` }))} style={{ minWidth: 300 }} /><div className="diff-grid section-card"><div><h4>当前正式版本 v{definition.version}</h4><pre>{json(definition)}</pre></div><div><h4>选定历史版本 v{selected?.version}</h4><pre>{json(selected)}</pre></div></div></Modal></>;
}
