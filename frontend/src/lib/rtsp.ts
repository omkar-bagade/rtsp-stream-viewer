export type ValidationResult = { ok: true; url: string } | { ok: false; error: string };

export interface ParsedRtspUrl {
  scheme: 'rtsp' | 'rtsps';
  user?: string;
  password?: string;
  host: string;
  port?: number;
  path: string;
  query: string;
}

// We parse RTSP URLs ourselves rather than with `new URL()`: older browsers
// treat non-special schemes (rtsp:) as opaque and return an empty hostname.
const RTSP_RE =
  /^(rtsps?):\/\/(?:([^:@/?#]*)(?::([^@/?#]*))?@)?(\[[0-9a-f:.]+\]|[^:/?#@[\]]+)(?::(\d{1,5}))?(\/[^?#]*)?(\?[^#]*)?$/i;

export function parseRtspUrl(value: string): ParsedRtspUrl | null {
  const m = RTSP_RE.exec(value.trim());
  if (!m) return null;
  const port = m[5] ? Number(m[5]) : undefined;
  if (port !== undefined && (port < 1 || port > 65535)) return null;
  return {
    scheme: m[1].toLowerCase() as 'rtsp' | 'rtsps',
    user: m[2] || undefined,
    password: m[3] || undefined,
    host: m[4],
    port,
    path: m[6] ?? '',
    query: m[7] ?? '',
  };
}

/** Client-side mirror of the backend's URL validation, for instant feedback. */
export function validateRtspUrl(input: string): ValidationResult {
  const value = input.trim();
  if (!value) return { ok: false, error: 'Enter an RTSP URL' };
  if (/\s/.test(value)) return { ok: false, error: 'URL must not contain spaces' };
  if (value.length > 2048) return { ok: false, error: 'URL is too long' };
  if (!/^rtsps?:\/\//i.test(value)) return { ok: false, error: 'URL must start with rtsp:// or rtsps://' };
  if (!parseRtspUrl(value)) return { ok: false, error: 'That doesn’t look like a valid RTSP URL (rtsp://host[:port]/path)' };
  return { ok: true, url: value };
}

/** Hides the password in rtsp://user:pass@host so it isn't shown on screen. */
export function redactUrl(value: string): string {
  return value.replace(/^(rtsps?:\/\/[^:/@]*):[^@/]*@/i, '$1:•••@');
}

/** A short, human-friendly default name: "host/path". */
export function defaultName(value: string): string {
  const p = parseRtspUrl(value);
  if (!p) return value;
  const path = p.path.replace(/^\/+|\/+$/g, '');
  return path ? `${p.host}/${path}` : p.host;
}

/** Scheme/host are case-insensitive; used for duplicate detection. */
export function normalizeUrl(value: string): string {
  const p = parseRtspUrl(value);
  if (!p) return value.trim();
  const auth = p.user ? `${p.user}${p.password ? ':' + p.password : ''}@` : '';
  return `${p.scheme}://${auth}${p.host.toLowerCase()}${p.port ? ':' + p.port : ''}${p.path}${p.query}`;
}
