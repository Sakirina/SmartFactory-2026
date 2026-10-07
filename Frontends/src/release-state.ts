import type { Contract } from './generated-client';
import { stringifyExactJSON } from './precision';

export const releaseStates: Record<string, string> = { active: '正在发布', pending: '等待前序批次', prepared: '已准备', applied: '已应用', running: '正在运行', failed: '处理失败', paused: '已暂停', completed: '历史发布完成', cancelled: '已取消', offline: '节点离线', waiting: '等待节点', restart_required: '需要重启' };
export const releaseActions: Record<string, string> = { pause: '暂停发布', resume: '继续发布', retry: '重新尝试', cancel: '取消后续发布', rollback: '退回旧发布' };
export function reportTiming(target: Contract<'ReleaseTarget'>, now: number) {
  const last = Number(target.last_report_ms ?? 0);
  const age = last > 0 ? Math.max(Number(target.report_age_ms ?? 0), now - last, 0) : null;
  return { age, fresh: target.report_fresh === true && age !== null && age <= 15000 };
}
export function workloadIssues(identity: Contract<'WorkloadIdentity'>, manifest: Contract<'ReleaseManifest'>) {
  const issues: string[] = [];
  const capabilities = new Set(identity.capabilities ?? []);
  if (!identity.enabled) issues.push('身份已停用');
  if (!capabilities.has('release') || identity.purpose !== 'release-runtime') issues.push('当前用途或能力未允许发布');
  if (identity.program !== manifest.program) issues.push('所属程序不同');
  const applicable = (manifest.components ?? []).filter(component => !component.target_node_ids?.length || component.target_node_ids.includes(identity.node_id));
  if (!applicable.some(component => component.kind === 'rule')) issues.push('此节点没有适用的规则');
  if (!applicable.some(component => !!component.configuration)) issues.push('此节点没有适用的配置');
  for (const component of manifest.components ?? []) {
    if (component.target_node_ids?.length && !component.target_node_ids.includes(identity.node_id)) continue;
    const needed = [...(component.required_capabilities ?? [])];
    if (component.kind === 'program' && component.build) needed.push('goos:' + component.build.goos, 'goarch:' + component.build.goarch);
    for (const capability of needed) if (!capabilities.has(capability)) issues.push('缺少能力 ' + capability);
    if (component.configuration) {
      const ids = component.configuration.kind === 'parameter' ? identity.parameter_ids : identity.connector_ids;
      if (!ids?.includes(component.configuration.id)) issues.push('未授权配置 ' + component.configuration.id);
    }
  }
  return [...new Set(issues)];
}
export function replaceProgram(manifest: Contract<'ReleaseManifest'>, artifact: Contract<'ReleaseArtifact'>): Contract<'ReleaseManifest'> {
  if (!artifact.build || !artifact.sha256) throw new Error('程序工件缺少构建信息或摘要');
  const prior = manifest.components?.find(component => component.kind === 'program');
  if (!prior) return { ...manifest, program: artifact.build.program as 'edge' | 'cloud', components: [{ id: 'program', kind: 'program', version: artifact.build.version, sha256: artifact.sha256, format: 'smartfactory-executable-v1', build: artifact.build }, ...manifest.components ?? []] };
  return { ...manifest, components: manifest.components?.map(component => component.kind === 'program'
    ? { ...component, sha256: artifact.sha256!, version: artifact.build!.version, build: artifact.build! }
    : { ...component, depends_on: component.depends_on?.map(dependency => dependency.id === prior.id ? { ...dependency, version: artifact.build!.version, sha256: artifact.sha256! } : dependency) ?? null }) ?? [] };
}
const escaped = (text: string) => text.replace(/</g, '\\u003c').replace(/>/g, '\\u003e').replace(/&/g, '\\u0026').replace(/\u2028/g, '\\u2028').replace(/\u2029/g, '\\u2029');
export function canonicalReleaseJSON(value: unknown): string {
  if (value === null || typeof value !== 'object') return escaped(typeof value === 'bigint' ? value.toString() : JSON.stringify(value));
  if (Array.isArray(value)) return '[' + value.map(item => canonicalReleaseJSON(item ?? null)).join(',') + ']';
  const encoder = new TextEncoder();
  const compare = (a: string, b: string) => { const x = encoder.encode(a), y = encoder.encode(b); for (let at = 0; at < Math.min(x.length, y.length); at++) if (x[at] !== y[at]) return x[at] - y[at]; return x.length - y.length; };
  return '{' + Object.keys(value).filter(key => (value as Record<string, unknown>)[key] !== undefined).sort(compare).map(key => escaped(JSON.stringify(key)) + ':' + canonicalReleaseJSON((value as Record<string, unknown>)[key])).join(',') + '}';
}
export async function releaseContentDigest(value: unknown) {
  const bytes = new TextEncoder().encode(canonicalReleaseJSON(value));
  const hash = await crypto.subtle.digest('SHA-256', bytes); return Array.from(new Uint8Array(hash), byte => byte.toString(16).padStart(2, '0')).join('');
}
export async function publicConnectorDigest(configuration: Contract<'ConnectorConfiguration'>) {
  // Public connector responses retain the Go model field order and the public
  // config map order. The backend hashes that fixed model including actor data.
  const bytes = new TextEncoder().encode(escaped(stringifyExactJSON(configuration)));
  const hash = await crypto.subtle.digest('SHA-256', bytes); return Array.from(new Uint8Array(hash), byte => byte.toString(16).padStart(2, '0')).join('');
}
export function actionReason(reason?: string) {
  return reason === 'publish permission is required' ? '当前身份需要发布权限' : reason === 'the current deployment state does not allow this action' ? '当前发布状态未允许此操作' : reason;
}
