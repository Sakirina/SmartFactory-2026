import { QueryClient } from '@tanstack/react-query';
import { useSyncExternalStore } from 'react';
import type { User } from './types';

export const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 500, gcTime: 300000, refetchOnWindowFocus: true, structuralSharing: false }, mutations: { retry: false } } });
let epoch = 0;
let identity = { key: 'anonymous:0', enabled: false };
const listeners = new Set<() => void>();
export function clearQueryIdentity() {
  epoch++;
  identity = { key: `anonymous:${epoch}`, enabled: false };
  void queryClient.cancelQueries();
  queryClient.clear();
  listeners.forEach(listener => listener());
  window.dispatchEvent(new Event('sf-cache-cleared'));
}
export function bindQueryIdentity(user: User) {
  const scope = JSON.stringify([user.id, user.version, [...user.roles].sort(), [...user.resources].sort(), [...(user.teams ?? [])].sort(), user.department_id, user.active]);
  const key = `${epoch}:${scope}`;
  if (identity.key !== key) { void queryClient.cancelQueries(); queryClient.clear(); }
  identity = { key, enabled: true };
  listeners.forEach(listener => listener());
}
export function useQueryIdentity() {
  return useSyncExternalStore(listener => { listeners.add(listener); return () => { listeners.delete(listener); }; }, () => identity);
}
export function currentQueryIdentity() { return identity; }
if (typeof window !== 'undefined') {
  window.addEventListener('sf-auth-expired', clearQueryIdentity);
  window.addEventListener('sf-authz-changed', clearQueryIdentity);
}
