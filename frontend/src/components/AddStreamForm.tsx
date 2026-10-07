import { useState, type FormEvent } from 'react';
import { api, type DemoStream } from '../lib/api';
import { defaultName, validateRtspUrl } from '../lib/rtsp';
import { PlusIcon } from './Icons';

interface Props {
  onAdd: (url: string, name: string) => void;
  isDuplicate: (url: string) => boolean;
  demoStreams: DemoStream[];
  disabledReason?: string;
}

export function AddStreamForm({ onAdd, isDuplicate, demoStreams, disabledReason }: Props) {
  const [url, setUrl] = useState('');
  const [name, setName] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (rawUrl: string, rawName: string) => {
    if (disabledReason) {
      setError(disabledReason);
      return;
    }
    const v = validateRtspUrl(rawUrl);
    if (!v.ok) {
      setError(v.error);
      return;
    }
    if (isDuplicate(v.url)) {
      setError('This stream is already in your grid');
      return;
    }
    setBusy(true);
    try {
      // Server-side validation (allowed hosts etc.). If the backend is
      // unreachable we still add the stream – its tile will show the error
      // and keep retrying.
      await api.validate(v.url);
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      if (!/abort|fetch|network|load failed/i.test(msg)) {
        setError(msg);
        setBusy(false);
        return;
      }
    }
    setBusy(false);
    onAdd(v.url, rawName.trim() || defaultName(v.url));
    setUrl('');
    setName('');
    setError(null);
  };

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    void submit(url, name);
  };

  return (
    <section className="add-panel" aria-label="Add a stream">
      <form className="add-form" onSubmit={onSubmit} noValidate>
        <div className="field field-url">
          <label htmlFor="rtsp-url">RTSP URL</label>
          <input
            id="rtsp-url"
            type="url"
            inputMode="url"
            autoComplete="off"
            spellCheck={false}
            placeholder="rtsp://user:pass@192.168.1.10:554/stream1"
            value={url}
            onChange={(e) => {
              setUrl(e.target.value);
              if (error) setError(null);
            }}
            aria-invalid={!!error}
            aria-describedby={error ? 'rtsp-url-error' : undefined}
          />
        </div>
        <div className="field field-name">
          <label htmlFor="rtsp-name">Name <span className="muted">(optional)</span></label>
          <input
            id="rtsp-name"
            type="text"
            placeholder="Front door"
            maxLength={60}
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </div>
        <button className="btn btn-primary" type="submit" disabled={busy}>
          <PlusIcon /> {busy ? 'Checking…' : 'Add stream'}
        </button>
      </form>
      {error && (
        <p id="rtsp-url-error" className="form-error" role="alert">
          {error}
        </p>
      )}
      {demoStreams.length > 0 && (
        <div className="demo-row">
          <span className="muted">Try a demo stream:</span>
          {demoStreams.map((d) => (
            <button
              key={d.url}
              type="button"
              className="chip"
              disabled={isDuplicate(d.url)}
              onClick={() => void submit(d.url, d.name)}
              title={d.url}
            >
              <PlusIcon width={14} height={14} /> {d.name}
            </button>
          ))}
          {demoStreams.some((d) => !isDuplicate(d.url)) && demoStreams.length > 1 && (
            <button
              type="button"
              className="chip chip-ghost"
              onClick={() => demoStreams.filter((d) => !isDuplicate(d.url)).forEach((d) => onAdd(d.url, d.name))}
            >
              Add all
            </button>
          )}
        </div>
      )}
    </section>
  );
}
