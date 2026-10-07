import { useState, type Dispatch, type SetStateAction } from 'react';
import { currentQueryIdentity } from './query-cache';

const edits = new Map<string, unknown>();
window.addEventListener('sf-cache-cleared', () => edits.clear());
export function useRetainedEdit<T>(name: string, initial: T): [T, Dispatch<SetStateAction<T>>] {
  const key = currentQueryIdentity().key + ':' + name;
  const [value, setValue] = useState<T>(() => edits.has(key) ? edits.get(key) as T : initial);
  const set: Dispatch<SetStateAction<T>> = next => setValue(previous => {
    const value = typeof next === 'function' ? (next as (old: T) => T)(previous) : next;
    edits.set(key, value); return value;
  });
  return [value, set];
}
