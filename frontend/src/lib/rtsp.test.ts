import { describe, expect, it } from 'vitest';
import { defaultName, normalizeUrl, parseRtspUrl, redactUrl, validateRtspUrl } from './rtsp';

describe('validateRtspUrl', () => {
  it.each([
    'rtsp://127.0.0.1:8554/cam1',
    'rtsps://user:p%40ss@cam.example.com:322/live?channel=1',
    'RTSP://Camera.local/stream',
    'rtsp://[fe80::1]:554/h264',
  ])('accepts %s', (url) => {
    expect(validateRtspUrl(url).ok).toBe(true);
  });

  it.each([
    ['', 'Enter an RTSP URL'],
    ['http://example.com/stream', 'rtsp://'],
    ['rtsp://', 'valid RTSP URL'],
    ['rtsp://host:99999/x', 'valid RTSP URL'],
    ['rtsp://host/with space', 'spaces'],
  ])('rejects %j', (url, msg) => {
    const r = validateRtspUrl(url);
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.error).toContain(msg);
  });
});

describe('helpers', () => {
  it('parses components', () => {
    expect(parseRtspUrl('rtsp://admin:secret@10.0.0.5:554/Streaming/101')).toMatchObject({
      scheme: 'rtsp',
      user: 'admin',
      password: 'secret',
      host: '10.0.0.5',
      port: 554,
      path: '/Streaming/101',
    });
  });

  it('redacts passwords', () => {
    expect(redactUrl('rtsp://admin:secret@10.0.0.5/live')).toBe('rtsp://admin:•••@10.0.0.5/live');
    expect(redactUrl('rtsp://10.0.0.5/live')).toBe('rtsp://10.0.0.5/live');
  });

  it('derives a default name', () => {
    expect(defaultName('rtsp://cam.local:554/front/door')).toBe('cam.local/front/door');
    expect(defaultName('rtsp://cam.local')).toBe('cam.local');
  });

  it('normalises scheme and host case for duplicate detection', () => {
    expect(normalizeUrl('RTSP://CAM.local/Path')).toBe(normalizeUrl('rtsp://cam.local/Path'));
    expect(normalizeUrl('rtsp://cam.local/a')).not.toBe(normalizeUrl('rtsp://cam.local/A'));
  });
});
