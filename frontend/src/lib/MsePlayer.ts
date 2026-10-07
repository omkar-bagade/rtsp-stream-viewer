/**
 * MsePlayer plays the backend's fragmented-MP4-over-WebSocket stream in a
 * <video> element using Media Source Extensions.
 *
 * Responsibilities:
 *  - WebSocket lifecycle with exponential-backoff reconnects
 *  - MediaSource / SourceBuffer setup from the codec announced by the server
 *  - An append queue (SourceBuffer accepts one append at a time)
 *  - Live-edge chasing: we're a live viewer, so if playback drifts behind the
 *    newest data we speed up slightly, or jump if it's far behind
 *  - Buffer trimming so long sessions don't grow memory unbounded
 *  - Stall detection (no data / frozen playback) → reconnect
 */

export type PlayerState =
  | 'idle'
  | 'connecting'
  | 'buffering'
  | 'live'
  | 'reconnecting'
  | 'paused'
  | 'error';

export interface PlayerStatus {
  state: PlayerState;
  message?: string;
  codec?: string;
  width?: number;
  height?: number;
  transcoded?: boolean;
  /** Seconds behind the newest received frame. */
  latency?: number;
  /** Incoming bitrate in kbit/s. */
  kbps?: number;
  /** True when the error won't be fixed by retrying (e.g. invalid URL). */
  fatal?: boolean;
}

type ServerMessage =
  | { type: 'status'; state: 'connecting' | 'live' | 'reconnecting' | 'error'; message?: string; retryInMs?: number }
  | { type: 'init'; codec: string; width: number; height: number; transcoded: boolean }
  | { type: 'error'; message: string };

interface InitInfo {
  codec: string;
  width: number;
  height: number;
  transcoded: boolean;
}

type MediaSourceCtor = typeof MediaSource;

function getMediaSource(): MediaSourceCtor | undefined {
  const w = window as unknown as { ManagedMediaSource?: MediaSourceCtor; MediaSource?: MediaSourceCtor };
  // ManagedMediaSource is the only MSE available on iPhone (iOS 17.1+).
  return w.MediaSource ?? w.ManagedMediaSource;
}

export function isMseSupported(): boolean {
  return getMediaSource() !== undefined;
}

const LIVE_TARGET_S = 0.4; // where we try to sit behind the live edge
const SPEEDUP_ABOVE_S = 1.0; // play at 1.1x when this far behind
const JUMP_ABOVE_S = 3.0; // seek to live edge when this far behind
const KEEP_BEHIND_S = 10; // seconds of history kept in the SourceBuffer
const MAX_QUEUE = 120; // queued segments before we resync (tab throttled, etc.)
const NO_DATA_TIMEOUT_MS = 20_000;
const MAX_RECONNECT_DELAY_MS = 15_000;

export class MsePlayer {
  private ws?: WebSocket;
  private mediaSource?: MediaSource;
  private sourceBuffer?: SourceBuffer;
  private objectUrl?: string;
  private queue: ArrayBuffer[] = [];
  private pendingInit?: InitInfo;
  private initInfo?: InitInfo;

  private running = false;
  private fatal = false;
  private reconnectAttempt = 0;
  private reconnectTimer?: number;
  private statsTimer?: number;
  private lastDataAt = 0;
  private bytesWindow = 0;
  private lastStatsAt = 0;
  private lastCurrentTime = -1;
  private frozenTicks = 0;
  private status: PlayerStatus = { state: 'idle' };
  private readonly onVideoError = () => this.handleVideoError();

  constructor(
    private readonly video: HTMLVideoElement,
    private readonly endpoint: string,
    private readonly rtspUrl: string,
    private readonly onStatus: (s: PlayerStatus) => void,
  ) {
    video.muted = true;
    video.playsInline = true;
    video.addEventListener('error', this.onVideoError);
  }

  /** Connects (or reconnects) and starts playing from the live edge. */
  play(): void {
    if (this.running) return;
    this.running = true;
    this.fatal = false;
    this.reconnectAttempt = 0;
    this.connect();
    this.statsTimer = window.setInterval(() => this.tick(), 1000);
  }

  /** Stops receiving data but keeps the last frame on screen. */
  pause(): void {
    this.running = false;
    this.closeSocket();
    this.clearTimers();
    this.queue = [];
    this.video.pause();
    this.emit({ state: 'paused', message: undefined, latency: undefined, kbps: undefined });
  }

  /** Forces a fresh connection (used by the Retry button). */
  restart(): void {
    this.running = false;
    this.closeSocket();
    this.clearTimers();
    this.teardownMedia();
    this.play();
  }

  destroy(): void {
    this.running = false;
    this.closeSocket();
    this.clearTimers();
    this.teardownMedia();
    this.video.removeEventListener('error', this.onVideoError);
  }

  // ---------------------------------------------------------------- socket

  private connect(): void {
    this.closeSocket();
    this.emit({ state: this.reconnectAttempt > 0 ? 'reconnecting' : 'connecting', message: 'Connecting to server…' });

    let ws: WebSocket;
    try {
      ws = new WebSocket(this.endpoint);
    } catch (err) {
      this.emit({ state: 'error', message: `Cannot open WebSocket: ${String(err)}`, fatal: true });
      return;
    }
    ws.binaryType = 'arraybuffer';
    this.ws = ws;
    this.lastDataAt = Date.now();

    ws.onopen = () => {
      ws.send(JSON.stringify({ type: 'subscribe', url: this.rtspUrl }));
    };
    ws.onmessage = (ev: MessageEvent<string | ArrayBuffer>) => {
      if (ws !== this.ws) return;
      this.lastDataAt = Date.now();
      if (typeof ev.data === 'string') this.handleControl(ev.data);
      else this.handleBinary(ev.data);
    };
    ws.onclose = () => {
      if (ws !== this.ws) return;
      this.ws = undefined;
      if (this.running && !this.fatal) this.scheduleReconnect('Connection to server lost');
    };
    // onerror is always followed by onclose; nothing to do here.
    ws.onerror = () => {};
  }

  private closeSocket(): void {
    const ws = this.ws;
    this.ws = undefined;
    if (ws) {
      ws.onopen = ws.onmessage = ws.onclose = ws.onerror = null;
      if (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING) ws.close(1000);
    }
  }

  private scheduleReconnect(reason: string): void {
    window.clearTimeout(this.reconnectTimer);
    const base = Math.min(1000 * 2 ** this.reconnectAttempt, MAX_RECONNECT_DELAY_MS);
    const delay = base / 2 + Math.random() * (base / 2); // jitter avoids thundering herd
    this.reconnectAttempt++;
    this.emit({ state: 'reconnecting', message: `${reason} — retrying in ${Math.ceil(delay / 1000)}s` });
    this.reconnectTimer = window.setTimeout(() => {
      if (this.running) this.connect();
    }, delay);
  }

  private handleControl(raw: string): void {
    let msg: ServerMessage;
    try {
      msg = JSON.parse(raw) as ServerMessage;
    } catch {
      return;
    }
    switch (msg.type) {
      case 'status':
        if (msg.state === 'live') {
          this.reconnectAttempt = 0;
          if (this.status.state !== 'live') this.emit({ state: this.sourceBuffer ? 'live' : 'buffering', message: undefined });
        } else {
          const retry = msg.retryInMs ? ` — retrying in ${Math.ceil(msg.retryInMs / 1000)}s` : '';
          this.emit({ state: msg.state, message: (msg.message ?? '') + retry, fatal: false });
        }
        break;
      case 'init':
        this.pendingInit = { codec: msg.codec, width: msg.width, height: msg.height, transcoded: msg.transcoded };
        break;
      case 'error':
        this.fatal = true;
        this.emit({ state: 'error', message: msg.message, fatal: true });
        this.closeSocket();
        break;
    }
  }

  private handleBinary(buf: ArrayBuffer): void {
    this.bytesWindow += buf.byteLength;
    if (this.pendingInit) {
      const info = this.pendingInit;
      this.pendingInit = undefined;
      this.setupMedia(info, buf);
      return;
    }
    if (!this.mediaSource) return; // media before init: ignore
    if (this.queue.length >= MAX_QUEUE) {
      // We can't keep up (e.g. background tab). Resync from the server's
      // keyframe cache instead of growing memory forever.
      this.queue = [];
      this.restart();
      return;
    }
    this.queue.push(buf);
    this.pump();
  }

  // ----------------------------------------------------------------- media

  private setupMedia(info: InitInfo, initSegment: ArrayBuffer): void {
    this.teardownMedia();
    const MS = getMediaSource();
    const mime = `video/mp4; codecs="${info.codec}"`;
    if (!MS) {
      this.failFatal('This browser does not support Media Source Extensions');
      return;
    }
    if (!MS.isTypeSupported(mime)) {
      this.failFatal(`This browser cannot play ${info.codec}`);
      return;
    }

    this.initInfo = info;
    const ms = new MS();
    this.mediaSource = ms;
    if (MS !== window.MediaSource) {
      // Required for ManagedMediaSource on Safari.
      (this.video as HTMLVideoElement & { disableRemotePlayback: boolean }).disableRemotePlayback = true;
    }
    this.objectUrl = URL.createObjectURL(ms);
    this.video.src = this.objectUrl;
    this.queue = [initSegment];

    ms.addEventListener(
      'sourceopen',
      () => {
        if (ms !== this.mediaSource) return;
        try {
          const sb = ms.addSourceBuffer(mime);
          sb.mode = 'segments';
          sb.addEventListener('updateend', () => this.onUpdateEnd(sb));
          sb.addEventListener('error', () => this.recover('SourceBuffer error'));
          this.sourceBuffer = sb;
          this.emit({
            state: 'buffering',
            message: undefined,
            codec: info.codec,
            width: info.width,
            height: info.height,
            transcoded: info.transcoded,
          });
          this.pump();
        } catch (err) {
          this.failFatal(`Cannot initialise decoder: ${String(err)}`);
        }
      },
      { once: true },
    );
  }

  private teardownMedia(): void {
    this.queue = [];
    this.sourceBuffer = undefined;
    this.pendingInit = undefined;
    const ms = this.mediaSource;
    this.mediaSource = undefined;
    if (ms && ms.readyState === 'open') {
      try {
        ms.endOfStream();
      } catch {
        /* ignore */
      }
    }
    if (this.objectUrl) {
      this.video.removeAttribute('src');
      this.video.load();
      URL.revokeObjectURL(this.objectUrl);
      this.objectUrl = undefined;
    }
  }

  private pump(): void {
    const sb = this.sourceBuffer;
    const ms = this.mediaSource;
    if (!sb || !ms || ms.readyState !== 'open' || sb.updating || this.queue.length === 0) return;
    const next = this.queue.shift()!;
    try {
      sb.appendBuffer(next);
    } catch (err) {
      if (err instanceof DOMException && err.name === 'QuotaExceededError') {
        // Buffer full: drop history and retry the same segment.
        this.queue.unshift(next);
        this.trim(true);
      } else {
        this.recover(`Append failed: ${String(err)}`);
      }
    }
  }

  private onUpdateEnd(sb: SourceBuffer): void {
    if (sb !== this.sourceBuffer) return;
    this.chaseLiveEdge();
    if (!sb.updating) this.trim(false);
    this.pump();
  }

  private chaseLiveEdge(): void {
    const v = this.video;
    const b = v.buffered;
    if (b.length === 0) return;
    const end = b.end(b.length - 1);
    const start = b.start(b.length - 1);

    // Initial start, or a gap (segments dropped server-side for a slow client).
    if (v.currentTime < start || v.currentTime > end) {
      v.currentTime = Math.max(start, end - LIVE_TARGET_S);
    }
    const lag = end - v.currentTime;
    if (lag > JUMP_ABOVE_S) {
      v.currentTime = end - LIVE_TARGET_S;
      v.playbackRate = 1;
    } else if (lag > SPEEDUP_ABOVE_S) {
      v.playbackRate = 1.1;
    } else if (lag < LIVE_TARGET_S) {
      v.playbackRate = 1;
    }

    if (this.running && v.paused) {
      v.play().catch(() => {
        /* autoplay can be blocked until user gesture; muted video normally allowed */
      });
    }
    if (this.running && this.status.state !== 'live' && this.status.state !== 'reconnecting' && v.readyState >= 2) {
      this.emit({ state: 'live', message: undefined });
    }
  }

  private trim(force: boolean): void {
    const sb = this.sourceBuffer;
    const b = this.video.buffered;
    if (!sb || sb.updating || b.length === 0) return;
    const keepFrom = this.video.currentTime - (force ? 2 : KEEP_BEHIND_S);
    const start = b.start(0);
    if (keepFrom - start > (force ? 0.5 : 5)) {
      try {
        sb.remove(start, keepFrom);
      } catch {
        /* ignore */
      }
    }
  }

  private handleVideoError(): void {
    if (!this.mediaSource) return;
    const err = this.video.error;
    this.recover(`Decoder error${err?.message ? `: ${err.message}` : ''}`);
  }

  /** Recoverable media failure: rebuild MediaSource from a fresh connection. */
  private recover(reason: string): void {
    if (!this.running) return;
    console.warn('[MsePlayer]', reason);
    this.teardownMedia();
    this.closeSocket();
    this.scheduleReconnect(reason);
  }

  private failFatal(message: string): void {
    this.fatal = true;
    this.closeSocket();
    this.teardownMedia();
    this.emit({ state: 'error', message, fatal: true });
  }

  // ----------------------------------------------------------------- stats

  private tick(): void {
    if (!this.running) return;
    const now = Date.now();
    const v = this.video;
    const b = v.buffered;

    // No bytes (not even status frames / pings answered) for too long.
    if (this.ws && now - this.lastDataAt > NO_DATA_TIMEOUT_MS && this.status.state === 'live') {
      this.recover('No data from server');
      return;
    }

    // Frozen playback while data is buffered ahead: nudge to the live edge.
    if (this.status.state === 'live' && b.length > 0) {
      const end = b.end(b.length - 1);
      if (v.currentTime === this.lastCurrentTime && end - v.currentTime > 0.5) {
        if (++this.frozenTicks >= 3) {
          v.currentTime = end - LIVE_TARGET_S;
          this.frozenTicks = 0;
        }
      } else {
        this.frozenTicks = 0;
      }
      this.lastCurrentTime = v.currentTime;
    }

    const elapsed = this.lastStatsAt ? (now - this.lastStatsAt) / 1000 : 1;
    const kbps = Math.round((this.bytesWindow * 8) / 1000 / elapsed);
    this.bytesWindow = 0;
    this.lastStatsAt = now;
    const latency = b.length ? Math.max(0, b.end(b.length - 1) - v.currentTime) : undefined;
    this.emit({ latency, kbps });
  }

  private clearTimers(): void {
    window.clearTimeout(this.reconnectTimer);
    window.clearInterval(this.statsTimer);
    this.reconnectTimer = this.statsTimer = undefined;
    this.lastStatsAt = 0;
  }

  private emit(patch: Partial<PlayerStatus>): void {
    this.status = { ...this.status, ...patch };
    if (patch.state && patch.state !== 'error') this.status.fatal = false;
    this.onStatus(this.status);
  }

  /** Info about the currently configured codec, if any. */
  get codecInfo(): InitInfo | undefined {
    return this.initInfo;
  }
}
