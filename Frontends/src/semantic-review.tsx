import { useState } from 'react';
import { useMutation } from '@tanstack/react-query';
import { Alert, Button, Descriptions, InputNumber, Modal, Space, Table, Tag } from 'antd';
import { callOperation, type Contract } from './generated-client';
import { json } from './api';
import type { Draft } from './types';
import { resourceLink } from './resource-navigation';

export function DraftSemanticReview({ draft, save }: { draft: Draft; save: () => Promise<Draft | null> }) {
  const [open, setOpen] = useState(false); const [published, setPublished] = useState(draft.base_version); const [budget, setBudget] = useState(512);
  const [diff, setDiff] = useState<Contract<'SemanticDifference'>>(); const [impact, setImpact] = useState<Contract<'ImpactAnalysis'>>(); const [reviewed, setReviewed] = useState('');
  const mutation = useMutation({ mutationFn: async () => {
    const saved = await save(); if (!saved) throw new Error('草稿保存完成后才能读取比较依据');
    const [difference, analysis] = await Promise.all([
      callOperation('post_drafts_id_semantic_diff', { parameters: { path: { id: saved.id } }, body: { expected_version: saved.version, published_version: published } }, { exact: true }),
      callOperation('post_drafts_id_impact', { parameters: { path: { id: saved.id } }, body: { expected_version: saved.version, budget } }, { exact: true }),
    ]);
    setDiff(difference); setImpact(analysis); setReviewed(json(saved.definition));
  } });
  const stale = reviewed && (reviewed !== json(draft.definition) || String(diff?.draft?.version) !== String(draft.version));
  return <><Button onClick={() => { setOpen(true); void mutation.mutateAsync().catch(() => {}); }}>语义差异与影响</Button><Modal title="草稿语义差异与影响" open={open} onCancel={() => setOpen(false)} width={1100} footer={<Button onClick={() => setOpen(false)}>关闭</Button>}>
    <Space wrap><span>比较发布版本</span><InputNumber aria-label="比较发布版本" min={0} value={published} onChange={value => setPublished(value ?? 0)} /><span>影响扫描预算</span><InputNumber aria-label="影响扫描预算" min={1} max={2000} value={budget} onChange={value => setBudget(value ?? 512)} /><Button loading={mutation.isPending} onClick={() => mutation.mutate()}>更新比较与影响</Button></Space>
    {mutation.error && <Alert type="error" title="比较未完成" description={mutation.error.message} />}{stale && <Alert type="warning" title="草稿已修改，请更新比较与影响" />}
    {diff && <><Descriptions className="section-card" column={2} items={[{ key: 'draft', label: '草稿依据', children: `${diff.draft?.id} / v${String(diff.draft?.version)} / ${diff.draft?.content_hash}` }, { key: 'published', label: '发布依据', children: `${diff.published?.id} / v${String(diff.published?.version)} / ${diff.published?.content_hash}` }]} /><Tag color={diff.equivalent ? 'green' : 'orange'}>{diff.equivalent ? '领域含义相同' : '存在语义变化'}</Tag><Table rowKey={change => `${change.category}:${change.path}:${change.change}`} size="small" dataSource={diff.changes ?? []} scroll={{ x: 900 }} columns={[{ title: '类别 / 位置', render: (_, change) => `${change.category} / ${change.path}` }, { title: '变化', dataIndex: 'change' }, { title: '类型', render: (_, change) => `${change.before_type} → ${change.after_type}` }, { title: '原值', render: (_, change) => <code>{json(change.before)}</code> }, { title: '当前值', render: (_, change) => <code>{json(change.after)}</code> }, { title: '单位', dataIndex: 'unit' }]} /></>}
    {impact && <><h3>影响范围</h3><Alert type={impact.complete ? 'success' : 'warning'} title={impact.complete ? '本次依赖遍历完整' : '本次依赖遍历存在未完成部分'} description={`已访问 ${impact.visited} 项，预算 ${impact.budget}`} /><Table rowKey={reference => `${reference.kind}:${reference.id}:${reference.location}`} size="small" dataSource={impact.references ?? []} columns={[{ title: '对象', render: (_, reference) => <a href={resourceLink(reference.kind, reference.id, reference.version)}>{reference.kind} / {reference.id}</a> }, { title: '版本', render: (_, reference) => String(reference.version) }, { title: '引用位置', dataIndex: 'location' }, { title: '经过', dataIndex: 'via' }]} />{(impact.issues ?? []).map((issue, index) => <Alert key={index} type="warning" title={issue.code} description={`${issue.location}：${issue.description}`} />)}</>}
  </Modal></>;
}
