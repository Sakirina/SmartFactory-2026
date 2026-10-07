import { useQuery } from '@tanstack/react-query';
import { readGeneratedResource } from './generated-client';
import { queryClient, useQueryIdentity } from './query-cache';
import { RequestError } from './transport';
import { stringifyExactJSON } from './precision';
import { useEffect, useMemo, useState } from 'react';
import { legacyScope, queryData, useWindowQuery } from './query';
export { api, post, session, RequestError } from './transport';

export function useResource<T>(path: string | null, refreshMS = 0) {
  const identity = useQueryIdentity();
  const [timeTick,setTimeTick] = useState(Date.now());
  useEffect(() => { if (!path?.startsWith('/data') || path.includes('from_ms')) return; const timer = window.setInterval(() => setTimeTick(Date.now()), 30000); return () => clearInterval(timer); }, [path]);
  const legacy = useMemo(() => legacyScope(path), [path,timeTick]);
  const windowResource = useWindowQuery(legacy.kind, legacy.scope, refreshMS);
  const queryKey = ['resource', identity.key, path];
  const result = useQuery({ queryKey, enabled: identity.enabled && !!path && !legacy.kind, queryFn: async ({ signal }) => { try { return await readGeneratedResource<T>(path!, signal); } catch (error) { if (error instanceof RequestError && [404,410].includes(error.status)) queryClient.setQueryData(queryKey, null); throw error; } }, refetchInterval: refreshMS === -1 ? 1000 : refreshMS > 0 ? refreshMS : false });
  if (legacy.kind) return { ...windowResource, data: (legacy.kind === 'trend' ? queryData(windowResource.data) : windowResource.data?.items?.map(row => row.data) ?? null) as T | null };
  return { data: identity.enabled ? result.data ?? null : null, error: result.error instanceof Error ? result.error.message : '', loading: result.isFetching, updatedAt: result.dataUpdatedAt, reload: () => result.refetch() };
}
export const timestamp = (ms?: number) => ms ? new Date(ms).toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' }) : '暂无记录';
export const shortTime = (ms?: number) => ms ? new Date(ms).toLocaleTimeString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' }) : '暂无记录';
export const json = (value: unknown) => stringifyExactJSON(value, 2);
