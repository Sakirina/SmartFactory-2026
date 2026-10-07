import type { Contract } from './generated-client';

export type QueryKind = 'entities' | 'alarms' | 'executions' | 'trend';
export type WindowState = { page: Contract<'QueryPage'>; cursor: string; revisions: Record<string, string>; appliedOrder?: bigint };
let windowOrder = 0n;
export const nextWindowOrder = () => ++windowOrder;
export function preferCommittedWindow(previous: WindowState | undefined, incoming: WindowState): WindowState {
  return previous && (previous.appliedOrder ?? 0n) > (incoming.appliedOrder ?? 0n) ? previous : incoming;
}
export const rowIdentity = (row: Pick<Contract<'QueryRow'>, 'kind' | 'id'>) => `${row.kind}:${row.id}`;
export function snapshotWindow(page: Contract<'QueryPage'>, cursor = page.snapshot_cursor ?? ''): WindowState {
  return { page, cursor, revisions: Object.fromEntries((page.items ?? []).map(row => [rowIdentity(row), row.revision ?? '0'])) };
}
export function applyWindowEvent(state: WindowState, event: Contract<'QueryEvent'>, eventID: string): WindowState {
  if (event.type === 'snapshot' && event.page) return snapshotWindow(event.page, eventID || event.cursor);
  if (event.type !== 'delta' && event.type !== 'checkpoint') return state;
  const revisions = { ...state.revisions };
  const rows = new Map((state.page.items ?? []).map(row => [rowIdentity(row), row]));
  for (const change of event.changes ?? []) {
    const id = rowIdentity(change);
    if (BigInt(change.revision ?? '0') < BigInt(revisions[id] ?? '0')) continue;
    revisions[id] = change.revision ?? '0';
    if (change.operation === 'remove') rows.delete(id);
    else if (change.item) rows.set(id, change.item);
  }
  const items = [...rows.values()].sort((a, b) => Number(b.sort_ms) - Number(a.sort_ms) || (String(a.id) < String(b.id) ? -1 : String(a.id) > String(b.id) ? 1 : 0));
  return { page: { ...state.page, items, metadata: event.metadata ?? state.page.metadata, has_more: event.has_more ?? state.page.has_more }, cursor: eventID || event.cursor || state.cursor, revisions };
}
