import { useEffect, useState } from 'react';
import { api, type ServerConfig } from '../lib/api';

/** False while the browser tab is hidden – streams pause to save bandwidth/CPU. */
export function usePageVisible(): boolean {
  const [visible, setVisible] = useState(() => document.visibilityState !== 'hidden');
  useEffect(() => {
    const on = () => setVisible(document.visibilityState !== 'hidden');
    document.addEventListener('visibilitychange', on);
    return () => document.removeEventListener('visibilitychange', on);
  }, []);
  return visible;
}

export type BackendState = 'checking' | 'online' | 'waking' | 'offline';

/**
 * Polls /healthz. Free-tier hosts (Render) sleep when idle and take ~30-60s
 * to wake; we surface that as "waking" instead of a scary error.
 */
export function useBackendHealth(intervalMs = 20_000) {
  const [state, setState] = useState<BackendState>('checking');
  const [activeStreams, setActiveStreams] = useState(0);

  useEffect(() => {
    let cancelled = false;
    let timer: number | undefined;
    let failures = 0;

    const check = async () => {
      try {
        const h = await api.health();
        if (cancelled) return;
        failures = 0;
        setState('online');
        setActiveStreams(h.activeStreams);
        timer = window.setTimeout(check, intervalMs);
      } catch {
        if (cancelled) return;
        failures++;
        setState(failures <= 4 ? 'waking' : 'offline');
        timer = window.setTimeout(check, Math.min(3000 * failures, intervalMs));
      }
    };
    void check();
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [intervalMs]);

  return { state, activeStreams };
}

export function useServerConfig(enabled: boolean): ServerConfig | null {
  const [cfg, setCfg] = useState<ServerConfig | null>(null);
  useEffect(() => {
    if (!enabled || cfg) return;
    api.config().then(setCfg).catch(() => {});
  }, [enabled, cfg]);
  return cfg;
}

/** useState persisted to localStorage (best effort). */
export function usePersistentState<T>(key: string, initial: T) {
  const [value, setValue] = useState<T>(() => {
    try {
      const raw = localStorage.getItem(key);
      return raw === null ? initial : (JSON.parse(raw) as T);
    } catch {
      return initial;
    }
  });
  useEffect(() => {
    try {
      localStorage.setItem(key, JSON.stringify(value));
    } catch {
      /* ignore */
    }
  }, [key, value]);
  return [value, setValue] as const;
}
