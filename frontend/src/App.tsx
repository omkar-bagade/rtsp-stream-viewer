import { useCallback, useMemo } from 'react';
import { AddStreamForm } from './components/AddStreamForm';
import { StreamTile } from './components/StreamTile';
import { CameraIcon, PauseIcon, PlayIcon } from './components/Icons';
import { useStreams } from './hooks/useStreams';
import { useBackendHealth, usePageVisible, usePersistentState, useServerConfig } from './hooks/misc';
import { wsUrl } from './lib/config';
import { isMseSupported } from './lib/MsePlayer';

type Layout = 'auto' | '1' | '2' | '3' | '4';
const LAYOUTS: { value: Layout; label: string }[] = [
  { value: 'auto', label: 'Auto' },
  { value: '1', label: '1×' },
  { value: '2', label: '2×' },
  { value: '3', label: '3×' },
  { value: '4', label: '4×' },
];

const BACKEND_LABEL = {
  checking: 'Checking server…',
  online: 'Server online',
  waking: 'Server waking up…',
  offline: 'Server unreachable',
} as const;

export default function App() {
  const { streams, has, add, remove, update, setAllPaused, move } = useStreams();
  const [layout, setLayout] = usePersistentState<Layout>('rtsp-viewer:layout', 'auto');
  const pageVisible = usePageVisible();
  const backend = useBackendHealth();
  const config = useServerConfig(backend.state === 'online');
  const endpoint = useMemo(() => wsUrl('/ws'), []);
  const mseOk = useMemo(isMseSupported, []);

  const togglePause = useCallback((id: string, paused: boolean) => update(id, { paused }), [update]);
  const rename = useCallback((id: string, name: string) => update(id, { name }), [update]);

  const maxTiles = config?.maxStreams ?? 16;
  const anyPlaying = streams.some((s) => !s.paused);

  return (
    <div className="app">
      <header className="topbar">
        <div className="brand">
          <span className="logo">
            <CameraIcon width={22} height={22} />
          </span>
          <div>
            <h1>RTSP Stream Viewer</h1>
            <p className="muted">Live camera streams in your browser · FFmpeg → WebSocket → MSE</p>
          </div>
        </div>
        <div className={`server-pill server-${backend.state}`} role="status" aria-live="polite">
          <span className="dot" aria-hidden />
          {BACKEND_LABEL[backend.state]}
          {backend.state === 'online' && backend.activeStreams > 0 && (
            <span className="muted"> · {backend.activeStreams} active upstream{backend.activeStreams === 1 ? '' : 's'}</span>
          )}
        </div>
      </header>

      {!mseOk && (
        <div className="banner error" role="alert">
          Your browser doesn’t support Media Source Extensions, which are required for live playback. Please use a
          recent Chrome, Edge, Firefox or Safari.
        </div>
      )}
      {backend.state === 'waking' && (
        <div className="banner">
          The streaming server is starting up (free hosting tiers sleep when idle). This can take up to a minute —
          streams will connect automatically.
        </div>
      )}

      <AddStreamForm
        onAdd={add}
        isDuplicate={has}
        demoStreams={config?.demoStreams ?? []}
        disabledReason={streams.length >= maxTiles ? `You can watch up to ${maxTiles} streams at once` : undefined}
      />

      {streams.length > 0 && (
        <div className="toolbar">
          <span className="muted">
            {streams.length} stream{streams.length === 1 ? '' : 's'}
          </span>
          <div className="toolbar-right">
            <button className="btn btn-sm" onClick={() => setAllPaused(anyPlaying)}>
              {anyPlaying ? <PauseIcon /> : <PlayIcon />} {anyPlaying ? 'Pause all' : 'Play all'}
            </button>
            <div className="segmented" role="radiogroup" aria-label="Grid columns">
              {LAYOUTS.map((l) => (
                <button
                  key={l.value}
                  role="radio"
                  aria-checked={layout === l.value}
                  className={layout === l.value ? 'active' : ''}
                  onClick={() => setLayout(l.value)}
                >
                  {l.label}
                </button>
              ))}
            </div>
          </div>
        </div>
      )}

      {streams.length === 0 ? (
        <section className="empty">
          <div className="empty-icon">
            <CameraIcon width={40} height={40} />
          </div>
          <h2>No streams yet</h2>
          <p className="muted">
            Paste an <code>rtsp://</code> URL above to start watching. Add several to view them side by side in a grid.
          </p>
        </section>
      ) : (
        <main className="grid" data-cols={layout}>
          {streams.map((s, i) => (
            <StreamTile
              key={s.id}
              stream={s}
              endpoint={endpoint}
              pageVisible={pageVisible}
              isFirst={i === 0}
              isLast={i === streams.length - 1}
              onTogglePause={togglePause}
              onRemove={remove}
              onRename={rename}
              onMove={move}
            />
          ))}
        </main>
      )}

      <footer className="page-footer muted">
        Tip: double-click a video for fullscreen, double-click a title to rename. Streams pause automatically while
        this tab is hidden.
      </footer>
    </div>
  );
}
