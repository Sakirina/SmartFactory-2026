import { parseAPIJSON, stringifyExactJSON } from './precision';

const tokenKey = 'smartfactory.session.' + location.host;
export const session = {
  get: () => sessionStorage.getItem(tokenKey) ?? '',
  set: (token: string) => sessionStorage.setItem(tokenKey, token),
  clear: () => sessionStorage.removeItem(tokenKey),
};
export class RequestError extends Error {
  constructor(public status: number, message: string, public code = '', public clear = false) { super(message); }
}
export async function api<T>(path: string, options: RequestInit = {}, decode: (text: string) => unknown = parseAPIJSON): Promise<T> {
  const response = await fetch('/api/sf/v1' + path, { ...options, headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + session.get(), ...options.headers } });
  const text = await response.text();
  const value = (text ? decode(text) : null) as T & { error?: string; detail?: string; code?: string; clear?: boolean };
  if (!response.ok) {
    if (response.status === 401) window.dispatchEvent(new Event('sf-auth-expired'));
    if (path !== '/me' && response.status === 403) window.dispatchEvent(new Event('sf-authz-changed'));
    throw new RequestError(response.status, value?.error || value?.detail || `请求失败 (${response.status})`, value?.code, value?.clear);
  }
  return value as T;
}
export const post = <T>(path: string, value: unknown = {}) => api<T>(path, { method: 'POST', body: stringifyExactJSON(value) });
