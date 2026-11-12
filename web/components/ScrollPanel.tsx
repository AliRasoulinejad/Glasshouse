'use client';
// The sticky right-hand panel. It shows the captured snapshot of whichever
// article section crosses the middle of the viewport, scrolling down or up.
// Snapshots are static files captured from a real Postgres (make web-capture).
import { useEffect, useState } from 'react';
import { SnapshotView, type Snapshot } from './SnapshotView';

const cache = new Map<string, Promise<Snapshot>>();

export function loadSnapshot(slug: string, step: string): Promise<Snapshot> {
  const url = `/snapshots/${slug}/${step}.json`;
  if (!cache.has(url)) {
    const p = fetch(url).then((r) => {
      if (!r.ok) throw new Error(`${url}: ${r.status}`);
      return r.json();
    }).then((env) => env.snapshot as Snapshot);
    p.catch(() => cache.delete(url)); // allow a retry after a failed fetch
    cache.set(url, p);
  }
  return cache.get(url)!;
}

export function useSnapshot(slug: string, step: string | null) {
  const [state, setState] = useState<{ step: string; snap?: Snapshot; error?: string } | null>(null);
  useEffect(() => {
    if (!step) return;
    let live = true;
    loadSnapshot(slug, step).then(
      (snap) => live && setState({ step, snap }),
      (e) => live && setState({ step, error: String(e) }),
    );
    return () => { live = false; };
  }, [slug, step]);
  return state;
}

export function ScrollPanel({ slug, steps }: { slug: string; steps: { id: string; label: string }[] }) {
  const [step, setStep] = useState<string>(steps[0].id);
  const state = useSnapshot(slug, step);

  useEffect(() => {
    // Warm the cache so scrolling never waits on the network.
    for (const s of steps) loadSnapshot(slug, s.id).catch(() => {});
    const sections = Array.from(document.querySelectorAll<HTMLElement>('[data-step]'));
    // A thin band at mid-viewport: the section crossing it is "in view".
    const io = new IntersectionObserver((entries) => {
      for (const e of entries) {
        if (e.isIntersecting) setStep((e.target as HTMLElement).dataset.step!);
      }
    }, { rootMargin: '-45% 0px -54% 0px' });
    sections.forEach((s) => io.observe(s));
    return () => io.disconnect();
  }, [slug, steps]);

  const index = steps.findIndex((s) => s.id === step);
  return (
    <aside className="scroll-panel" aria-live="polite">
      <p className="small muted panel-step">
        Step {index + 1} of {steps.length} · {steps[index]?.label} · captured from a real Postgres 17
      </p>
      {state?.snap ? <SnapshotView snapshot={state.snap} />
        : state?.error ? <p className="small muted">Could not load this step's snapshot.</p>
          : <p className="small muted">Loading…</p>}
    </aside>
  );
}

// On narrow screens the sticky panel is hidden and each step's diagram is
// shown inline under the section that starts it, loaded once it scrolls near.
export function InlineDiagram({ slug, step }: { slug: string; step: string }) {
  const [want, setWant] = useState(false);
  const [el, setEl] = useState<HTMLDivElement | null>(null);
  useEffect(() => {
    if (!el) return;
    const narrow = window.matchMedia('(max-width: 960px)');
    const io = new IntersectionObserver(([e]) => {
      if (e.isIntersecting && narrow.matches) { setWant(true); io.disconnect(); }
    }, { rootMargin: '400px 0px' });
    io.observe(el);
    return () => io.disconnect();
  }, [el]);
  const state = useSnapshot(slug, want ? step : null);
  return (
    <div className="inline-diagram" ref={setEl}>
      {state?.snap && <SnapshotView snapshot={state.snap} />}
    </div>
  );
}
