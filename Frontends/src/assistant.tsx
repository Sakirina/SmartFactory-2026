import { useEffect, useRef, useState } from 'react';
import { useMutation } from '@tanstack/react-query';
import { Alert, Button, Card, Descriptions, Drawer, Input, Space, Table, Tag } from 'antd';
import { json, useResource } from './api';
import { callOperation, streamAssistant, type Contract } from './generated-client';
import { queryClient, useQueryIdentity } from './query-cache';
import { parseExactJSON, exactTimestamp } from './precision';
import type { User } from './types';
import { PageTitle } from './views';
import { resourceLink } from './resource-navigation';

const statusLabels: Record<string,string> = { pending: '正在取得与检查依据', supported: '所有引用均可访问', partial: '部分引用当前可访问', unsupported: '当前引用均不可用', missing: '回答尚未引用依据' };
const referenceLabels: Record<string,string> = { valid: '可以访问', unknown: '未知依据身份', cross_investigation: '引用来自其他调查', expired: '依据保留期限已结束', deleted: '内容或资源已删除', forbidden: '当前资源授权已失效', tool_error: '工具执行失败', budget_exceeded: '工具结果超过预算', empty: '工具结果为空', not_delivered: '工具结果尚未提供给模型' };
type AssistantEvent = Contract<'AssistantEvent'>;
export function Assistant({ user, initialID = '' }: { user: User; initialID?: string }) {
  const identity = useQueryIdentity(); const [input, setInput] = useState(''); const [selected, setSelected] = useState(initialID); const [after, setAfter] = useState(''); const [pages, setPages] = useState<string[]>([]);
  const investigations = useResource<Contract<'InvestigationList'>>('/investigations?limit=20' + (after ? '&after=' + encodeURIComponent(after) : ''), 5000);
  const investigation = useResource<Contract<'Investigation'>>(selected ? '/investigations/' + encodeURIComponent(selected) : null, 2000);
  const [provisional, setProvisional] = useState(''); const [tools, setTools] = useState<string[]>([]); const [currentInput, setCurrentInput] = useState(''); const [evidenceID, setEvidenceID] = useState('');
  const evidence = useResource<Contract<'InvestigationEvidence'>>(selected && evidenceID ? '/investigations/' + encodeURIComponent(selected) + '/evidence/' + encodeURIComponent(evidenceID) : null);
  const active = useRef<AbortController | undefined>(undefined);
  useEffect(() => { if (initialID) setSelected(initialID); }, [initialID]);
  useEffect(() => { const clear = () => { active.current?.abort(); setProvisional(''); setTools([]); setCurrentInput(''); setEvidenceID(''); }; window.addEventListener('sf-cache-cleared', clear); return () => { active.current?.abort(); window.removeEventListener('sf-cache-cleared', clear); }; }, []);
  const send = useMutation({ mutationFn: async () => {
    if (!input.trim()) throw new Error('请输入调查内容');
    const content = input.trim(); setCurrentInput(content); setInput(''); setProvisional(''); setTools([]); setEvidenceID('');
    const controller = new AbortController(); active.current = controller; let id = ''; let completed = false;
    try {
      const body = await streamAssistant([{ role: 'user', content }], controller.signal); const reader = body.getReader(); const decoder = new TextDecoder(); let pending = '';
      for (;;) {
        const chunk = await reader.read(); if (chunk.done) break; pending += decoder.decode(chunk.value, { stream: true }).replace(/\r\n/g,'\n'); let end: number;
        while ((end = pending.indexOf('\n\n')) >= 0) {
          const frame = pending.slice(0,end); pending = pending.slice(end + 2); const data = frame.split('\n').filter(line => line.startsWith('data:')).map(line => line.slice(5).trimStart()).join('\n'); if (!data || data === '[DONE]') continue;
          const event = parseExactJSON(data) as AssistantEvent;
          if (event.investigation_id) { id = event.investigation_id; setSelected(id); }
          if (event.type === 'tool') setTools(old => [...old, `${event.name} / ${event.tool_call_id}`]);
          if (event.type === 'delta') setProvisional(old => old + (event.text ?? ''));
          if (event.type === 'final') setProvisional('');
          if (event.type === 'error') throw new Error(event.error || '调查服务返回错误');
          if (event.type === 'done') { completed = true; await reader.cancel(); break; }
        }
        if (completed) break;
      }
      if (!completed) throw new Error('调查连接已中断，正在读取保存的运行状态');
    } finally {
      controller.abort(); active.current = undefined;
      if (id && identity.enabled) {
        const saved = await callOperation('get_investigations_id', { parameters: { path: { id } } }, { exact: true });
        queryClient.setQueryData(['resource', identity.key, '/investigations/' + encodeURIComponent(id)], saved);
      }
      await investigations.reload();
    }
  } });
  const remove = useMutation({ mutationFn: async () => { await callOperation('delete_investigations_id', { parameters: { path: { id: selected } } }); setSelected(''); setEvidenceID(''); setProvisional(''); setCurrentInput(''); await investigations.reload(); } });
  const result = investigation.data;
  const inaccessible = !!result?.evidence?.some(item=>['deleted','expired','forbidden'].includes(item.access_status ?? ''));
  useEffect(()=>{ if (inaccessible || result?.status==='deleted') { active.current?.abort(); setCurrentInput(''); setProvisional(''); setTools([]); } },[inaccessible,result?.status]);
  const unavailableEvidence = ['deleted','expired','forbidden'].includes(result?.evidence?.find(item=>item.id===evidenceID)?.access_status ?? '');
  const visibleEvidence = !evidence.error && !unavailableEvidence ? evidence.data : null;
  return <><PageTitle title="AI 调查" note="保存实际工具查询、模型取得的原文与引用状态，逐项查看当前可访问的规则、趋势和执行依据。" actions={<Button onClick={() => { void investigation.reload(); void investigations.reload(); void evidence.reload(); }}>更新依据状态</Button>} />
    <div className="assistant-layout"><Card className="chat-panel"><div className="chat-history" aria-live="polite">{(currentInput || result?.messages?.length) && <div className="chat-message message-user"><strong>{user.name}</strong><div>{currentInput || result?.messages?.filter(message => message.role === 'user').at(-1)?.content}</div></div>}
      {send.isPending && tools.length > 0 && <div className="investigation-tools">{tools.map((tool,index) => <p key={index}>{tool}</p>)}</div>}
      {(provisional || result) && <div className="chat-message message-assistant"><strong>平台助手</strong><div>{send.isPending ? provisional || '正在组织调查结果…' : result?.answer || (result?.status === 'deleted' ? '此调查内容已经删除' : '当前未提供可访问的回答内容')}</div></div>}
      {result && <><Tag>{result.status}</Tag><Tag color={result.evidence_status === 'supported' ? 'green' : 'orange'}>{statusLabels[result.evidence_status]}</Tag><p>调查身份 {result.id}，保留至 {exactTimestamp(result.expires_ms)}</p>
        <Table rowKey={(_, index) => String(index)} size="small" pagination={false} dataSource={result.references ?? []} columns={[{ title: '引用身份', dataIndex: 'id' }, { title: '当前状态', dataIndex: 'status', render: status => referenceLabels[status] || status }, { title: '依据详情', render: (_, reference) => <Button disabled={reference.status !== 'valid'} onClick={() => setEvidenceID(reference.id)}>查看依据原文</Button> }]} />
        <h3>实际工具依据</h3><Table rowKey="id" size="small" dataSource={result.evidence ?? []} pagination={false} columns={[{ title: '工具 / 调用身份', render: (_, item) => `${item.tool_name} / ${item.tool_call_id}` }, { title: '状态', render: (_, item) => `${item.status} / ${item.delivery} / ${item.access_status || '待检查'}` }, { title: '原文 / 预算字节', render: (_, item) => `${String(item.visible_bytes)} / ${String(item.budget_bytes)}` }, { title: '详情', render: (_, item) => <Button disabled={!!item.access_status && item.access_status !== 'valid'} onClick={() => setEvidenceID(item.id)}>查看工具依据</Button> }]} />
      </>}{result?.error && <Alert type="error" title="调查运行未完成" description={result.error} />}{(send.error || investigation.error || remove.error) && <Alert type="error" title="调查操作未完成" description={send.error?.message || investigation.error || remove.error?.message} />}
    </div><div className="chat-composer"><Input.TextArea aria-label="发送给 AI 助手的消息" value={input} onChange={event => setInput(event.target.value)} placeholder="输入需要调查的现象、时间或资源身份" rows={3} /><div><span>每次发送保存独立调查与工具依据。</span><Button type="primary" aria-label="发送" loading={send.isPending} disabled={!input.trim()} onClick={() => send.mutate()}>发送</Button></div></div></Card>
    <aside><Card title="已保存调查">{investigations.error && <Alert type="error" title="调查目录读取未完成" description={investigations.error} />}{investigations.data?.items?.map(item => <button className="investigation-item" key={item.id} onClick={() => { setSelected(item.id); setCurrentInput(''); setProvisional(''); setEvidenceID(''); send.reset(); }}><strong>{item.id}</strong><small>{exactTimestamp(item.created_ms)} / {item.status}</small></button>)}<Space wrap><Button disabled={!pages.length} onClick={() => { setAfter(pages.at(-1) ?? ''); setPages(pages.slice(0,-1)); }}>上一页调查</Button><Button disabled={!investigations.data?.has_more} onClick={() => { setPages([...pages,after]); setAfter(investigations.data?.next_after ?? ''); }}>下一页调查</Button></Space></Card>{result && <Card title="调查内容管理" className="section-card"><Button danger disabled={send.isPending || result.status === 'running'} loading={remove.isPending} onClick={() => remove.mutate()}>删除此调查内容</Button></Card>}</aside></div>
    <Drawer title="模型实际取得的依据" open={!!evidenceID} onClose={() => setEvidenceID('')} extra={<Button onClick={() => { void evidence.reload(); void investigation.reload(); }}>更新当前依据状态</Button>} width="min(1050px,100vw)">{evidence.error && <Alert type="error" title="当前依据无法读取" description={evidence.error} />}{visibleEvidence && <><Descriptions column={2} items={[{ key: 'id', label: '依据身份', children: visibleEvidence.id }, { key: 'call', label: '实际工具调用', children: visibleEvidence.tool_call_id }, { key: 'state', label: '工具状态', children: `${visibleEvidence.status} / ${visibleEvidence.delivery}` }, { key: 'bytes', label: '字节与预算', children: `${String(visibleEvidence.visible_bytes)} / ${String(visibleEvidence.budget_bytes)}` }, { key: 'sha', label: '可见原文 SHA256', children: visibleEvidence.visible_sha256 }]} /><Space wrap className="section-actions">{visibleEvidence.resources?.map(resource => <a key={`${resource.kind}:${resource.id}`} href={resourceLink(resource.kind, resource.id, resource.version)}>{resource.kind} / {resource.id} / v{String(resource.version)}</a>)}</Space><h3>调用参数</h3><pre>{json(visibleEvidence.arguments)}</pre><h3>进入模型的 JSON 原文</h3><pre data-testid="model-visible-content" className="json-view">{visibleEvidence.visible_content}</pre></>}</Drawer>
  </>;
}
