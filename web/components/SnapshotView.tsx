'use client';
// Maps snapshot.type to a view, like the Inspector's views/registry.js.
// postgres.heap_and_index is what the Inspector emits today; the index tree
// and running-queries parts are ported from postgres-heap-and-index.js and
// running-queries.js.
import { useLayoutEffect, useRef, useState } from 'react';
import { HeapView, type HeapData } from './HeapView';

type IndexItem = { itemoffset: number; ctid: string; dead: boolean; data_hex?: string; value?: string | null };
type IndexPage = { block: number; type: string; level: number; items: IndexItem[] };
type IndexData = { index_name: string; pages: IndexPage[]; truncated?: boolean };
type Query = { pid: number; state: string; wait_event_type?: string; wait_event?: string; duration_ms: number; query: string };
export type Snapshot = { type: string; data: any };

// The child block of an internal page item's "(block,offset)" downlink;
// the offset is not meaningful there (see btreepage.go's downlinkBlock).
function downlinkBlock(ctid: string): number | null {
  const m = /^\((\d+),\d+\)$/.exec(ctid ?? '');
  return m ? Number(m[1]) : null;
}

function IndexTree({ index }: { index: IndexData }) {
  const wrap = useRef<HTMLDivElement>(null);
  const boxes = useRef(new Map<number, HTMLDivElement>());
  const [lines, setLines] = useState<{ x1: number; y1: number; x2: number; y2: number }[]>([]);

  // Connectors need the boxes' real rendered positions, so draw after layout
  // and again whenever the panel resizes.
  useLayoutEffect(() => {
    const el = wrap.current;
    if (!el) return;
    const draw = () => {
      const known = new Set(index.pages.map((p) => p.block));
      const base = el.getBoundingClientRect();
      const next = [];
      for (const page of index.pages) {
        if (page.level === 0) continue;
        for (const item of page.items) {
          const child = downlinkBlock(item.ctid);
          const pe = boxes.current.get(page.block);
          const ce = child === null ? undefined : boxes.current.get(child);
          if (child === null || !known.has(child) || !pe || !ce) continue;
          const p = pe.getBoundingClientRect();
          const c = ce.getBoundingClientRect();
          next.push({ x1: p.left + p.width / 2 - base.left, y1: p.bottom - base.top, x2: c.left + c.width / 2 - base.left, y2: c.top - base.top });
        }
      }
      setLines(next);
    };
    draw();
    const ro = new ResizeObserver(draw);
    ro.observe(el);
    return () => ro.disconnect();
  }, [index]);

  return (
    <div className="index-tree" ref={wrap}>
      <svg className="index-tree-lines" aria-hidden="true">
        {lines.map((l, i) => <line key={i} {...l} />)}
      </svg>
      <div className="index-level">
        {index.pages.map((page) => (
          <div className="index-page" key={page.block} ref={(el) => { if (el) boxes.current.set(page.block, el); else boxes.current.delete(page.block); }}>
            <p className="small muted" style={{ margin: '0 0 4px' }}>
              Block {page.block} · {page.type} · level {page.level} · {page.items.length} items
            </p>
            <div className="index-items">
              {page.items.map((item) => (
                <span key={item.itemoffset} className={'index-chip' + (item.dead ? ' dead' : '')}
                  title={`itemoffset=${item.itemoffset} ctid=${item.ctid}` + (item.value ? ` value=${item.value}` : '') +
                    (item.data_hex ? ` data_hex=${item.data_hex}` : '') + (item.dead ? ' (dead)' : '')}>
                  <span className="index-chip-value">{item.value ? item.value : item.data_hex ? item.data_hex.slice(0, 23) : '(none)'}</span>
                  <span className="index-chip-tid">{item.ctid}</span>
                </span>
              ))}
            </div>
          </div>
        ))}
      </div>
      {index.truncated && <p className="small muted">More index pages exist beyond what is shown here.</p>}
    </div>
  );
}

function RunningQueries({ queries }: { queries?: Query[] }) {
  const qs = queries ?? [];
  return (
    <>
      <h3 style={{ margin: '20px 0 8px' }}>Running queries ({qs.length})</h3>
      {qs.length === 0 ? <p className="small muted">No active backends right now.</p> : (
        <table className="queries">
          <thead><tr><th>PID</th><th>State</th><th>Wait event</th><th>Duration</th><th>Query</th></tr></thead>
          <tbody>
            {qs.map((q) => (
              <tr key={q.pid}>
                <td>{q.pid}</td><td>{q.state}</td>
                <td>{q.wait_event_type ? `${q.wait_event_type}: ${q.wait_event}` : ''}</td>
                <td>{q.duration_ms} ms</td>
                <td>{(q.query ?? "").length > 140 ? `${q.query.slice(0, 140)}…` : q.query}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  );
}

export function SnapshotView({ snapshot }: { snapshot: Snapshot }) {
  const d = snapshot.data;
  switch (snapshot.type) {
    case 'postgres.heap_and_index':
      return (
        <div className="heap-and-index">
          <HeapView data={d.heap as HeapData} />
          <h3 style={{ margin: '20px 0 8px' }}>Index: {d.index.index_name}</h3>
          <IndexTree index={d.index as IndexData} />
          <RunningQueries queries={d.queries} />
        </div>
      );
    case 'postgres.heap_page':
      return <div><HeapView data={d as HeapData} /><RunningQueries queries={d.queries} /></div>;
    default:
      return <p className="small muted">No view for snapshot type {snapshot.type}.</p>;
  }
}
