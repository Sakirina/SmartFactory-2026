import { useState } from 'react';
import { Alert, App, Button, Descriptions, Input, Modal, Select, Space, Table, Tag } from 'antd';
import { api, json, post, timestamp } from './api';
import type { Definition, Draft, Json } from './types';

export { DraftSimulation } from './simulation';

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
