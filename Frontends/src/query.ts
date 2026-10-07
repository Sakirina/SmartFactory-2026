import { useEffect, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { callOperation, streamQuery, type Contract } from './generated-client';
import { queryClient, useQueryIdentity } from './query-cache';
import { RequestError, session } from './transport';
import { parseExactJSON, stringifyExactJSON } from './precision';
import { applyWindowEvent, snapshotWindow, nextWindowOrder, preferCommittedWindow, type QueryKind, type WindowState } from './query-window';
import type { DataResult, Entity, Json } from './types';

export async function queryPage(kind: QueryKind, body: Contract<'QueryRequest'>, signal?: AbortSignal) {
  return callOperation('post_queries_kind', { parameters: { path: { kind } }, body }, { signal, exact: true });
}
export function useWindowQuery(kind: QueryKind | null, scope: Contract<'QueryRequest'>, refreshMS = 0) {
  const identity = useQueryIdentity();
  const scopeKey = stringifyExactJSON(scope);
  const queryKey = ['window', identity.key, kind, scopeKey];
  const [streamError, setStreamError] = useState('');
  const [streamStatus, setStreamStatus] = useState('查询');
  const [online,setOnline] = useState(()=>navigator.onLine);
  useEffect(()=>{ const changed = () => setOnline(navigator.onLine); window.addEventListener('online',changed); window.addEventListener('offline',changed); return()=>{ window.removeEventListener('online',changed); window.removeEventListener('offline',changed); }; },[]);
  const result = useQuery({ queryKey, enabled: identity.enabled && !!kind, queryFn: async ({ signal }) => { const appliedOrder = nextWindowOrder(); return { ...snapshotWindow(await queryPage(kind!, parseExactJSON(scopeKey) as Contract<'QueryRequest'>, signal)), appliedOrder }; }, structuralSharing: (previous, incoming) => preferCommittedWindow(previous as WindowState | undefined, incoming as WindowState), refetchInterval: refreshMS > 0 ? refreshMS : false, refetchOnWindowFocus: refreshMS !== -1 });
  useEffect(() => {
    setStreamError('');
    if (!identity.enabled || !kind || refreshMS !== -1 || scope.page_token) { setStreamStatus('查询'); return; }
    if (!online) { setStreamStatus('等待网络恢复'); return; }
    const controller = new AbortController(); let timer: number | undefined;
    const connect = async () => {
      const previous = queryClient.getQueryData<WindowState>(queryKey);
      setStreamStatus(previous?.cursor ? '恢复订阅' : '建立订阅');
      try {
        const body = await streamQuery(kind, parseExactJSON(scopeKey) as Contract<'QueryRequest'>, previous?.cursor ?? '', controller.signal);
        const reader = body.getReader(); const decoder = new TextDecoder(); let pending = '';
        for (;;) {
          const chunk = await reader.read(); if (chunk.done) break;
          pending += decoder.decode(chunk.value, { stream: true }).replace(/\r\n/g, '\n');
          let end: number;
          while ((end = pending.indexOf('\n\n')) >= 0) {
            const block = pending.slice(0, end); pending = pending.slice(end + 2);
            const lines = block.split('\n');
            const text = lines.filter(line => line.startsWith('data:')).map(line => line.slice(5).trimStart()).join('\n');
            if (!text) { setStreamStatus('订阅已连接'); continue; }
            const event = parseExactJSON(text) as Contract<'QueryEvent'>;
            const eventID = lines.find(line => line.startsWith('id:'))?.slice(3).trim() ?? '';
            if (event.type === 'reset') {
              if (event.clear) { void queryClient.cancelQueries(); queryClient.clear(); }
              if (/(authorization|session|authentication)/.test(event.reason ?? '')) window.dispatchEvent(new Event('sf-authz-changed'));
              else await result.refetch();
              setStreamError('订阅重新取得快照：' + (event.reason ?? '查询状态变化'));
              await reader.cancel();
              if (event.retryable === false) return;
              break;
            }
            if (!controller.signal.aborted) queryClient.setQueryData<WindowState>(queryKey, old => {
              const next = event.type === 'snapshot' && event.page ? snapshotWindow(event.page, eventID || event.cursor) : old ? applyWindowEvent(old, event, eventID) : undefined;
              return next ? { ...next, appliedOrder: nextWindowOrder() } : undefined;
            });
            setStreamStatus('订阅已连接'); setStreamError('');
          }
        }
      } catch (error) {
        if (!controller.signal.aborted) {
          if (error instanceof RequestError && error.status === 401) window.dispatchEvent(new Event('sf-auth-expired'));
          else if (error instanceof RequestError && error.status === 403) window.dispatchEvent(new Event('sf-authz-changed'));
          setStreamError(error instanceof Error ? error.message : String(error));
        }
      }
      if (!controller.signal.aborted) { setStreamStatus('等待恢复'); timer = window.setTimeout(() => void connect(), 1000); }
    };
    void connect();
    return () => { controller.abort(); if (timer) clearTimeout(timer); };
  }, [identity.key, identity.enabled, kind, scopeKey, refreshMS, online]);
  const resetRequired = result.error instanceof RequestError && result.error.clear;
  useEffect(()=>{ if (resetRequired) { void queryClient.cancelQueries(); queryClient.clear(); } },[result.error]);
  const reload = async () => { setStreamError(''); return result.refetch(); };
  return { data: identity.enabled && !resetRequired ? result.data?.page : undefined, resetRequired, cursor: result.data?.cursor ?? '', error: result.error instanceof Error ? result.error.message : streamError, loading: result.isFetching, updatedAt: result.dataUpdatedAt, reload, streamStatus };
}
export async function queryAllPages(kind: QueryKind, scope: Contract<'QueryRequest'>, signal?: AbortSignal) {
  const items: Contract<'QueryRow'>[] = []; let pageToken: string | undefined;
  for (;;) {
    const page = await queryPage(kind, { ...scope, page_token: pageToken }, signal); items.push(...page.items ?? []);
    if (!page.has_more || !page.next_page_token) return { ...page, items };
    pageToken = page.next_page_token;
  }
}
export function queryData(page?: Contract<'QueryPage'>): DataResult | null {
  if (!page) return null;
  return { points: (page.items ?? []).map(row => row.data) as DataResult['points'], quality: (page.metadata?.quality ?? {good:0,bad:0,uncertain:0,excluded:0,missing:null,completeness:'unknown'}) as DataResult['quality'], sources: (page.metadata?.sources ?? []) as DataResult['sources'], revisions: (page.metadata?.revisions ?? []) as DataResult['revisions'], gaps: page.metadata?.gaps as DataResult['gaps'], data_version: 0, truncated: !!page.has_more };
}
export function legacyScope(path: string | null): { kind: QueryKind | null; scope: Contract<'QueryRequest'> } {
  const [pathname, search] = (path ?? '').split('?'); const params = new URLSearchParams(search);
  if (pathname === '/entities' || pathname === '/alarms' || pathname === '/executions') return { kind: pathname.slice(1) as QueryKind, scope: { limit: 500 } };
  if (pathname !== '/data') return { kind: null, scope: {} };
  return { kind: 'trend', scope: { resource_ids: params.get('device_ids')?.split(',') ?? [], keys: params.get('keys')?.split(',') ?? [], from_ms: Number(params.get('from_ms')) || Date.now() - (Number(params.get('window_ms')) || 3600000), to_ms: Number(params.get('to_ms')) || Date.now() + 30000, resolution: params.get('resolution') || 'raw', limit: Math.min(Number(params.get('limit')) || 100, 2000) } };
}
