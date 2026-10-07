/**
 * Backend location. Empty VITE_BACKEND_URL means "same origin" — used when the
 * Go server also serves this app, and in dev via the Vite proxy.
 */
const raw = (import.meta.env.VITE_BACKEND_URL ?? '').trim().replace(/\/+$/, '');

export const API_BASE = raw;

export function apiUrl(path: string): string {
  return `${API_BASE}${path}`;
}

export function wsUrl(path = '/ws'): string {
  if (API_BASE) return API_BASE.replace(/^http/i, 'ws') + path;
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${proto}//${window.location.host}${path}`;
}
