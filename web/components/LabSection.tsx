'use client';
// "Try it yourself": the reader's own Postgres, started on demand by the
// gateway (same origin, /api and /auth). Buttons only: the reader picks an
// action name, never SQL. Everything from the API is rendered as React text,
// never as HTML.
import { useCallback, useEffect, useRef, useState } from 'react';
import { SnapshotView, type Snapshot } from './SnapshotView';

type ActionInfo = { name: string; description: string; query: string };
type Lab = { state: 'none' | 'queued' | 'starting' | 'ready' | 'failed'; position?: number; actions?: ActionInfo[]; expires_at?: string };
type Me = { provider: string; name: string };
type Line = { kind: 'cmd' | 'out' | 'err'; text: string };

// Sent on every state-changing request; the gateway refuses them without it.
const ACTION_HEADER = { 'X-Glasshouse-Action': '1' };

async function api<T>(path: string, init?: RequestInit): Promise<{ status: number; body: T | null }> {
  const r = await fetch(path, { credentials: 'same-origin', ...init });
  const text = await r.text();
  let body: T | null = null;
  try { body = text ? JSON.parse(text) : null; } catch { body = null; }
  return { status: r.status, body };
}

function resultText(result: unknown): string {
  if (result && typeof result === 'object' && 'tag' in result) return String((result as { tag: unknown }).tag);
  return JSON.stringify(result);
}

export function LabSection({ actions: wanted }: { actions: string[] }) {
  const [me, setMe] = useState<Me | 'anon' | 'unavailable' | null>(null);
  const [lab, setLab] = useState<Lab>({ state: 'none' });
  const [snap, setSnap] = useState<Snapshot | null>(null);
  const [lines, setLines] = useState<Line[]>([]);
  const [busy, setBusy] = useState(false);
  const term = useRef<HTMLPreElement>(null);
  const log = (l: Line) => setLines((ls) => [...ls.slice(-200), l]);

  useEffect(() => {
    api<Me>('/api/me').then(
      ({ status, body }) => setMe(status === 200 && body ? body : status === 401 ? 'anon' : 'unavailable'),
      () => setMe('unavailable'),
    );
  }, []);

  const refreshLab = useCallback(async () => {
    const { status, body } = await api<Lab>('/api/lab');
    if (status === 200 && body) setLab(body);
  }, []);

  useEffect(() => { if (me && typeof me === 'object') refreshLab(); }, [me, refreshLab]);

  // Poll while the lab is queued or starting.
  useEffect(() => {
    if (lab.state !== 'queued' && lab.state !== 'starting') return;
    const t = setInterval(refreshLab, 2000);
    return () => clearInterval(t);
  }, [lab.state, refreshLab]);

  const refreshSnap = useCallback(async () => {
    const { status, body } = await api<{ snapshot: Snapshot }>('/api/lab/snapshot');
    if (status === 200 && body) setSnap(body.snapshot);
    else if (status === 404) { setLab({ state: 'none' }); setSnap(null); }
  }, []);

  // Live view: refetch the snapshot whenever the lab reports an event.
  useEffect(() => {
    if (lab.state !== 'ready') return;
    refreshSnap();
    let timer: ReturnType<typeof setTimeout> | undefined;
    const es = new EventSource('/api/lab/events');
    es.addEventListener('event', () => {
      clearTimeout(timer);
      timer = setTimeout(refreshSnap, 300);
    });
    return () => { clearTimeout(timer); es.close(); };
  }, [lab.state, refreshSnap]);

  useEffect(() => { term.current?.scrollTo(0, term.current.scrollHeight); }, [lines]);

  async function start() {
    setBusy(true);
    const { status, body } = await api<Lab & { error?: string }>('/api/lab', { method: 'POST', headers: ACTION_HEADER });
    setBusy(false);
    if (status === 200 && body) setLab(body);
    else log({ kind: 'err', text: body?.error ?? `Could not start a lab (HTTP ${status}).` });
  }

  async function stop() {
    await api('/api/lab', { method: 'DELETE', headers: ACTION_HEADER });
    setLab({ state: 'none' });
    setSnap(null);
    setLines([]);
  }

  async function run(a: ActionInfo) {
    setBusy(true);
    log({ kind: 'cmd', text: `glasshouse=> ${a.query};` });
    try {
      const { status, body } = await api<{ result?: unknown; error?: string }>(`/api/lab/actions/${encodeURIComponent(a.name)}`, {
        method: 'POST', headers: ACTION_HEADER,
      });
      if (status === 200 && body) log({ kind: 'out', text: resultText(body.result) });
      else log({ kind: 'err', text: `ERROR:  ${body?.error ?? `HTTP ${status}`}` });
    } catch {
      log({ kind: 'err', text: 'ERROR:  the lab did not answer' });
    }
    setBusy(false);
    refreshSnap();
  }

  async function signOut() {
    await api('/auth/logout', { method: 'POST', headers: ACTION_HEADER });
    setMe('anon');
    setLab({ state: 'none' });
  }

  const next = typeof window === 'undefined' ? '/' : window.location.pathname;
  const available = (lab.actions ?? []).filter((a) => wanted.includes(a.name));

  return (
    <section className="lab" id="try-it-yourself" aria-labelledby="lab-heading">
      <h2 id="lab-heading">Try it yourself</h2>
      <p>
        Everything above was captured from a fresh Postgres. Here you get your own: a separate
        PostgreSQL 17 just for you, started on demand and deleted after about 15 minutes idle.
        You press buttons; each one runs one fixed statement, shown below as it runs.
      </p>

      {me === null && <p className="small muted">Checking sign-in…</p>}
      {me === 'unavailable' && <p className="small muted">The live lab is not available on this copy of the site.</p>}
      {me === 'anon' && (
        <div className="lab-signin">
          <a className="button primary" href={`/auth/github/login?next=${encodeURIComponent(next)}`}>Sign in with GitHub</a>
          <a className="button" href={`/auth/google/login?next=${encodeURIComponent(next)}`}>Sign in with Google</a>
          <p className="small muted">Sign-in only identifies you so each reader gets one lab. Nothing else is stored.</p>
        </div>
      )}

      {me && typeof me === 'object' && (
        <>
          <p className="small muted">
            Signed in with {me.provider} as {me.name}. <button className="linkish" onClick={signOut}>Sign out</button>
          </p>
          {(lab.state === 'none' || lab.state === 'failed') && (
            <p>
              {lab.state === 'failed' && <span className="small bad">Your last lab failed to start. </span>}
              <button className="primary" onClick={start} disabled={busy}>Start my lab</button>
            </p>
          )}
          {lab.state === 'queued' && <p>All labs are busy. You are number {lab.position} in the queue…</p>}
          {lab.state === 'starting' && <p>Starting your Postgres… this takes a few seconds.</p>}
          {lab.state === 'ready' && (
            <div className="lab-live">
              <div className="lab-controls">
                <div className="actions">
                  {available.map((a) => (
                    <button key={a.name} onClick={() => run(a)} disabled={busy} title={a.description}>{a.name}</button>
                  ))}
                </div>
                <pre className="terminal" ref={term} aria-label="Terminal output" aria-live="polite">
                  {lines.length === 0 ? 'glasshouse=> ' : lines.map((l, i) => (
                    <span key={i} className={`term-${l.kind}`}>{l.text}{'\n'}</span>
                  ))}
                </pre>
                <button onClick={stop}>Stop my lab</button>
              </div>
              <div className="lab-diagram">
                {snap ? <SnapshotView snapshot={snap} /> : <p className="small muted">Loading your database…</p>}
              </div>
            </div>
          )}
        </>
      )}
      {lines.length > 0 && lab.state !== 'ready' && <p className="small bad">{lines[lines.length - 1].text}</p>}
    </section>
  );
}
