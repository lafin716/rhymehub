import { useCallback, useEffect, useState, type Dispatch, type SetStateAction } from 'react';
import { errorMessage } from '../api/client';

export interface AsyncResult<T> {
  data: T | undefined;
  error: string | null;
  loading: boolean;
  reload: () => void;
  setData: Dispatch<SetStateAction<T | undefined>>;
}

/**
 * Runs `load` on mount and whenever `deps` change or `reload` is called. Stale responses
 * (from superseded loads) are dropped.
 */
export function useAsync<T>(load: () => Promise<T>, deps: readonly unknown[] = []): AsyncResult<T> {
  const [data, setData] = useState<T | undefined>(undefined);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [tick, setTick] = useState(0);

  useEffect(() => {
    let active = true;
    setLoading(true);
    load()
      .then((value) => {
        if (!active) return;
        setData(value);
        setError(null);
      })
      .catch((err: unknown) => {
        if (active) setError(errorMessage(err));
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
    // `load` is intentionally excluded: callers pass the inputs that matter via `deps`.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tick, ...deps]);

  const reload = useCallback(() => setTick((n) => n + 1), []);
  return { data, error, loading, reload, setData };
}
