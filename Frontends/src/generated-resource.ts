import { useQuery } from '@tanstack/react-query';
import { useQueryIdentity } from './query-cache';

export function useGeneratedResource<T>(key: string | null, loader: (signal: AbortSignal) => Promise<T>, refreshMS = 0) {
  const identity = useQueryIdentity();
  const result = useQuery({ queryKey: ['generated', identity.key, key], enabled: identity.enabled && !!key, queryFn: ({ signal }) => loader(signal), refetchInterval: refreshMS === -1 ? 1000 : refreshMS > 0 ? refreshMS : false });
  return { data: identity.enabled ? result.data : undefined, error: result.error instanceof Error ? result.error.message : '', loading: result.isFetching, reload: () => result.refetch() };
}
