import { memo, useEffect, useRef, useState } from 'react';
import { MsePlayer, type PlayerStatus } from '../lib/MsePlayer';
import { redactUrl } from '../lib/rtsp';
import type { StreamEntry } from '../hooks/useStreams';
import {
  AlertIcon,
  ChevronLeft,
  ChevronRight,
  CloseIcon,
  ExpandIcon,
  PauseIcon,
  PlayIcon,
  RetryIcon,
} from './Icons';

interface Props {
  stream: StreamEntry;
  endpoint: string;
  pageVisible: boolean;
  isFirst: boolean;
  isLast: boolean;
  onTogglePause: (id: string, paused: boolean) => void;
  onRemove: (id: string) => void;
  onRename: (id: string, name: string) => void;
  onMove: (id: string, delta: -1 | 1) => void;
}

const STATE_LABEL: Record<PlayerStatus['state'], string> = {
  idle: 'Idle',
  connecting: 'Connecting',
  buffering: 'Buffering',
  live: 'Live',
  reconnecting: 'Reconnecting',
  paused: 'Paused',
  error: 'Error',
};

function codecLabel(codec: string): string {
  if (/^avc[13]/i.test(codec)) return 'H.264';
  if (/^(hev1|hvc1)/i.test(codec)) return 'H.265';
  if (/^vp09/i.test(codec)) return 'VP9';
  return codec;
}

function StreamTileImpl({
  stream,
  endpoint,
  pageVisible,
  isFirst,
  isLast,
  onTogglePause,
  onRemove,
  onRename,
  onMove,
}: Props) {
  const frameRef = useRef<HTMLDivElement>(null);
  const videoRef = useRef<HTMLVideoElement>(null);
  const playerRef = useRef<MsePlayer | null>(null);
  const [status, setStatus] = useState<PlayerStatus>({ state: 'idle' });
  const [editing, setEditing] = useState(false);
  const [hasFrame, setHasFrame] = useState(false);

  // One player per tile for its whole lifetime.
  useEffect(() => {
    const video = videoRef.current!;
    const player = new MsePlayer(video, endpoint, stream.url, (s) => setStatus({ ...s }));
    playerRef.current = player;
    const onFrame = () => setHasFrame(true);
    video.addEventListener('loadeddata', onFrame);
    return () => {
      video.removeEventListener('loadeddata', onFrame);
      player.destroy();
      playerRef.current = null;
    };
  }, [endpoint, stream.url]);

  // Run only when the user wants it AND the tab is visible.
  const shouldPlay = !stream.paused && pageVisible;
  useEffect(() => {
    const p = playerRef.current;
    if (!p) return;
    if (shouldPlay) p.play();
    else p.pause();
  }, [shouldPlay, endpoint, stream.url]);

  const toggleFullscreen = () => {
    const el = frameRef.current;
    if (!el) return;
    if (document.fullscreenElement) void document.exitFullscreen();
    else void el.requestFullscreen?.().catch(() => {});
  };

  const { state } = status;
  const showSpinner = !stream.paused && (state === 'connecting' || state === 'buffering' || state === 'reconnecting');
  const showError = !stream.paused && state === 'error';
  const resolution = status.width && status.height ? `${status.width}×${status.height}` : null;

  return (
    <article className={`tile tile-${stream.paused ? 'paused' : state}`} aria-label={stream.name}>
      <header className="tile-header">
        <span className={`badge badge-${stream.paused ? 'paused' : state}`}>
          <span className="dot" aria-hidden />
          {stream.paused ? 'Paused' : STATE_LABEL[state]}
        </span>
        {editing ? (
          <input
            className="tile-name-input"
            autoFocus
            defaultValue={stream.name}
            maxLength={60}
            aria-label="Stream name"
            onBlur={(e) => {
              onRename(stream.id, e.target.value.trim() || stream.name);
              setEditing(false);
            }}
            onKeyDown={(e) => {
              if (e.key === 'Enter') (e.target as HTMLInputElement).blur();
              if (e.key === 'Escape') setEditing(false);
            }}
          />
        ) : (
          <h3 className="tile-name" title={`${stream.name} — double-click to rename`} onDoubleClick={() => setEditing(true)}>
            {stream.name}
          </h3>
        )}
        <div className="tile-order">
          <button className="icon-btn sm" disabled={isFirst} onClick={() => onMove(stream.id, -1)} aria-label="Move left" title="Move left">
            <ChevronLeft />
          </button>
          <button className="icon-btn sm" disabled={isLast} onClick={() => onMove(stream.id, 1)} aria-label="Move right" title="Move right">
            <ChevronRight />
          </button>
        </div>
        <button className="icon-btn sm danger" onClick={() => onRemove(stream.id)} aria-label={`Remove ${stream.name}`} title="Remove">
          <CloseIcon />
        </button>
      </header>

      <div className="tile-frame" ref={frameRef} onDoubleClick={toggleFullscreen}>
        <video ref={videoRef} muted playsInline autoPlay disablePictureInPicture />

        {!hasFrame && !showError && !showSpinner && (
          <div className="overlay subtle">
            <span className="muted">{stream.paused ? 'Paused — press play to watch' : 'Waiting for video…'}</span>
          </div>
        )}
        {showSpinner && (
          <div className="overlay">
            <div className="spinner" aria-hidden />
            <span>{status.message || STATE_LABEL[state] + '…'}</span>
          </div>
        )}
        {showError && (
          <div className="overlay error" role="alert">
            <AlertIcon width={28} height={28} />
            <span>{status.message || 'Stream unavailable'}</span>
            <button className="btn btn-sm" onClick={() => playerRef.current?.restart()}>
              <RetryIcon /> Retry now
            </button>
          </div>
        )}
        {stream.paused && hasFrame && (
          <button className="overlay overlay-btn" onClick={() => onTogglePause(stream.id, false)} aria-label="Resume">
            <span className="big-play">
              <PlayIcon width={30} height={30} />
            </span>
          </button>
        )}
      </div>

      <footer className="tile-controls">
        <button
          className="icon-btn"
          onClick={() => onTogglePause(stream.id, !stream.paused)}
          aria-label={stream.paused ? 'Play' : 'Pause'}
          title={stream.paused ? 'Play' : 'Pause'}
        >
          {stream.paused ? <PlayIcon /> : <PauseIcon />}
        </button>
        <button className="icon-btn" onClick={() => playerRef.current?.restart()} aria-label="Reconnect" title="Reconnect" disabled={stream.paused}>
          <RetryIcon />
        </button>
        <div className="tile-meta" title={redactUrl(stream.url)}>
          {state === 'live' && !stream.paused ? (
            <>
              {resolution && <span>{resolution}</span>}
              {status.codec && <span title={status.codec}>{codecLabel(status.codec)}{status.transcoded ? ' · transcoded' : ''}</span>}
              {status.latency !== undefined && <span>{status.latency.toFixed(1)}s buffer</span>}
              {status.kbps !== undefined && <span>{status.kbps >= 1000 ? `${(status.kbps / 1000).toFixed(1)} Mbps` : `${status.kbps} kbps`}</span>}
            </>
          ) : (
            <span className="url">{redactUrl(stream.url)}</span>
          )}
        </div>
        <button className="icon-btn" onClick={toggleFullscreen} aria-label="Fullscreen" title="Fullscreen">
          <ExpandIcon />
        </button>
      </footer>
    </article>
  );
}

export const StreamTile = memo(StreamTileImpl);
