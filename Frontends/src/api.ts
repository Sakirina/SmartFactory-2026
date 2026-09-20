import { useCallback, useEffect, useRef, useState } from 'react';
import { parseAPIJSON } from './precision';

const tokenKey = 'smartfactory.session.' + location.host;
export const session = { get: () => sessionStorage.getItem(tokenKey) ?? '', set: (token: string) => sessionStorage.setItem(tokenKey, token), clear: () => sessionStorage.removeItem(tokenKey) };
export class RequestError extends Error { constructor(public status: number, message: string) { super(message); } }
export async function api<T>(path: string, options: RequestInit = {}): Promise<T> {
  const response = await fetch('/api/sf/v1' + path, { ...options, headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + session.get(), ...options.headers } });
  const value = parseAPIJSON(await response.text()) as T & { error?: string };
  if (!response.ok) { if (response.status === 401) window.dispatchEvent(new Event('sf-auth-expired')); throw new RequestError(response.status, value.error || `请求失败 (${response.status})`); }
  return value as T;
}
export const post = <T>(path: string, value: unknown = {}) => api<T>(path, { method: 'POST', body: JSON.stringify(value) });
export function useResource<T>(path: string | null, refreshMS = 0) {
  const [data, setData] = useState<T | null>(null); const [error, setError] = useState(''); const [loading, setLoading] = useState(false); const [updatedAt, setUpdatedAt] = useState(0); const generation = useRef(0);
  const inflight = useRef<{ path: string; controller: AbortController } | null>(null);
  const reload = useCallback(async () => {
    if (!path || inflight.current?.path === path) return;
    inflight.current?.controller.abort();
    const controller = new AbortController(); inflight.current = { path, controller };
    const current = ++generation.current; setLoading(true);
    try { const result = await api<T>(path, { signal: controller.signal }); if (current === generation.current) { setData(result); setError(''); setUpdatedAt(Date.now()); } }
    catch (e) { if (current === generation.current && !controller.signal.aborted) setError(e instanceof Error ? e.message : String(e)); }
    finally { if (inflight.current?.controller === controller) inflight.current = null; if (current === generation.current) setLoading(false); }
  }, [path]);
  useEffect(() => {
    setData(null); setError('');
    const controller = new AbortController();
    const streaming = refreshMS === -1 && path?.startsWith('/data');
    if (!streaming) void reload();
    const interval = refreshMS === -1 ? 1000 : refreshMS;
    const timer = !streaming && interval > 0 ? window.setInterval(() => void reload(), interval) : undefined;
    let reconnect: number | undefined;
    const connect = async () => {
      if (!path) return;
      try {
        const response = await fetch('/api/sf/v1' + path.replace(/^\/data/, '/events'), { headers: { Authorization: 'Bearer ' + session.get() }, signal: controller.signal });
        if (!response.ok) { if (response.status === 401) window.dispatchEvent(new Event('sf-auth-expired')); throw new Error(`数据订阅失败 (${response.status})`); }
        const reader = response.body!.getReader(); const decoder = new TextDecoder(); let pending = '';
        for (;;) {
          const chunk = await reader.read(); if (chunk.done) break;
          pending += decoder.decode(chunk.value, { stream: true });
          const frames = pending.split('\n\n'); pending = frames.pop() ?? '';
          for (const frame of frames) {
            const line = frame.split('\n').find(value => value.startsWith('data: ')); if (!line) continue;
            const result = parseAPIJSON(line.slice(6)) as T & { error?: string };
            if (result.error) throw new Error(result.error);
            if (!controller.signal.aborted) { setData(result); setError(''); setUpdatedAt(Date.now()); }
          }
        }
        if (!controller.signal.aborted) throw new Error('数据订阅已中断，正在重新连接');
      } catch (error) { if (!controller.signal.aborted) setError(error instanceof Error ? error.message : String(error)); }
      if (!controller.signal.aborted) reconnect = window.setTimeout(() => void connect(), 1000);
    };
    if (streaming) void connect();
    return () => { generation.current++; controller.abort(); inflight.current?.controller.abort(); inflight.current = null; if (timer) window.clearInterval(timer); if (reconnect) window.clearTimeout(reconnect); };
  }, [reload, refreshMS, path]);
  return { data, error, loading, updatedAt, reload };
}
export const timestamp = (ms?: number) => ms ? new Date(ms).toLocaleString('zh-CN', { hour12: false }) : '暂无记录';
export const shortTime = (ms?: number) => ms ? new Date(ms).toLocaleTimeString('zh-CN', { hour12: false }) : '暂无记录';
export const json = (value: unknown) => JSON.stringify(value, null, 2);
