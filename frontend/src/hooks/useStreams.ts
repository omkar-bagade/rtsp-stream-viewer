import { useCallback, useEffect, useState } from 'react';
import { normalizeUrl } from '../lib/rtsp';

export interface StreamEntry {
  id: string;
  name: string;
  url: string;
  paused: boolean;
  addedAt: number;
}

const STORAGE_KEY = 'rtsp-viewer:streams:v1';

function load(): StreamEntry[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return [];
    const parsed = JSON.parse(raw) as unknown;
    if (!Array.isArray(parsed)) return [];
    return parsed.filter(
      (s): s is StreamEntry =>
        typeof s === 'object' && s !== null && typeof s.id === 'string' && typeof s.url === 'string',
    );
  } catch {
    return [];
  }
}

function newId(): string {
  return typeof crypto !== 'undefined' && 'randomUUID' in crypto
    ? crypto.randomUUID()
    : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
}

/** The user's stream list, persisted in localStorage so it survives reloads. */
export function useStreams() {
  const [streams, setStreams] = useState<StreamEntry[]>(load);

  useEffect(() => {
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(streams));
    } catch {
      /* storage unavailable (private mode) – keep working in memory */
    }
  }, [streams]);

  const has = useCallback(
    (url: string) => streams.some((s) => normalizeUrl(s.url) === normalizeUrl(url)),
    [streams],
  );

  const add = useCallback((url: string, name: string) => {
    setStreams((prev) => [...prev, { id: newId(), url: url.trim(), name, paused: false, addedAt: Date.now() }]);
  }, []);

  const remove = useCallback((id: string) => {
    setStreams((prev) => prev.filter((s) => s.id !== id));
  }, []);

  const update = useCallback((id: string, patch: Partial<Omit<StreamEntry, 'id'>>) => {
    setStreams((prev) => prev.map((s) => (s.id === id ? { ...s, ...patch } : s)));
  }, []);

  const setAllPaused = useCallback((paused: boolean) => {
    setStreams((prev) => prev.map((s) => ({ ...s, paused })));
  }, []);

  const move = useCallback((id: string, delta: -1 | 1) => {
    setStreams((prev) => {
      const i = prev.findIndex((s) => s.id === id);
      const j = i + delta;
      if (i < 0 || j < 0 || j >= prev.length) return prev;
      const next = prev.slice();
      [next[i], next[j]] = [next[j], next[i]];
      return next;
    });
  }, []);

  const clear = useCallback(() => setStreams([]), []);

  return { streams, has, add, remove, update, setAllPaused, move, clear };
}
