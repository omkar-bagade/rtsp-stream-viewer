import { apiUrl } from './config';

export interface DemoStream {
  name: string;
  url: string;
}

export interface ServerConfig {
  demoStreams: DemoStream[];
  maxStreams: number;
  maxViewersPerStream: number;
}

export interface Health {
  status: string;
  uptimeSeconds: number;
  activeStreams: number;
}

async function request<T>(path: string, init?: RequestInit, timeoutMs = 10_000): Promise<T> {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), timeoutMs);
  try {
    const res = await fetch(apiUrl(path), { ...init, signal: ctrl.signal });
    const body = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error((body as { error?: string }).error ?? `Request failed (${res.status})`);
    return body as T;
  } finally {
    clearTimeout(timer);
  }
}

export const api = {
  health: () => request<Health>('/healthz', undefined, 8_000),
  config: () => request<ServerConfig>('/api/config'),
  validate: (url: string) =>
    request<{ id: string; url: string }>('/api/streams/validate', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ url }),
    }),
};
