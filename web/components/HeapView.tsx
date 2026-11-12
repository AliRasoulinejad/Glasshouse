'use client';
// React port of inspector/web/static/views/postgres-heap-page.js. Byte map of
// each heap page (8 KiB), drawn from real pageinspect output:
//
//   [0, 24)            page header
//   [24, lower)        item pointers, 4 bytes each
//   [lower, upper)     free space
//   [upper, special)   tuples, packed from the end of the page
//   [special, size)    special space (empty for heap pages)
//
// A tuple with t_xmax set is dead even while its pointer is still LP_NORMAL.
import { useState } from 'react';

const BYTES_PER_ROW = 128;
const CELL_W = 4;
const ROW_H = 8;

const COLORS = {
  header: '#5b7db1',
  pointer: '#9bb3d6',
  free: 'var(--cell)',
  tupleA: '#2f8f6f',
  tupleB: '#4fae8f',
  dead: '#8a8a84',
};

export type HeapItem = {
  lp: number; lp_off: number; lp_flags: number; lp_len: number;
  t_xmin: string; t_xmax: string; t_ctid: string;
  t_infomask: number; t_infomask2: number; t_hoff: number;
  t_bits: string; t_data_hex: string; id?: number | null; payload?: string | null;
};
export type HeapPage = {
  block: number; free_space: number; items: HeapItem[];
  header: Record<string, string | number> & { lower: number; upper: number; special: number; page_size: number };
};
export type HeapData = { relation: string; pages: HeapPage[] };

type Detail = { title: string; rows: [string, string][] };

// Hover text for detail-table field names (ASD-STE100 wording, as in the
// Inspector viewer).
const FIELD_INFO: Record<string, string> = {
  lsn: 'The LSN identifies the last WAL record that changed this page.',
  checksum: 'The checksum value protects the page data. It is 0 if checksums are off.',
  flags: 'The flags show the page status. For example, they show if free space is registered.',
  lower: 'The lower value shows where the item pointer list ends and free space starts.',
  upper: 'The upper value shows where free space ends and tuple data starts.',
  special: 'The special value shows where the special area starts. For a heap page, this area is empty.',
  page_size: 'The page size is the total size of the page in bytes. The default value is 8192.',
  version: 'The version number shows the layout format of the page.',
  prune_xid: 'The prune_xid value is the oldest transaction ID that may still need pruning on this page.',
  free_space: 'The free_space value is the number of bytes open for new pointers and tuple data.',
  lp: 'The lp value is the position of this pointer in the item pointer list. The block number and the lp value together identify the row.',
  lp_off: 'The lp_off value is the byte offset of the tuple data in the page.',
  lp_len: 'The lp_len value is the length of the tuple in bytes. It is 0 if the pointer is not in use.',
  lp_flags: 'The lp_flags value shows the pointer state: 0 not in use, 1 normal, 2 redirect, 3 dead.',
  t_xmin: 'The t_xmin value is the ID of the transaction that added this tuple.',
  t_xmax: 'The t_xmax value is the ID of the transaction that removed or changed this tuple. 0 means the tuple is still live.',
  t_ctid: 'The t_ctid value points to the current version of this tuple. It points to a new tuple if this row was updated.',
  t_infomask: 'The t_infomask value is a set of flags about the tuple, for example commit status.',
  t_infomask2: 'The t_infomask2 value is a second set of flags. It also stores the number of columns.',
  t_hoff: 'The t_hoff value is the length of the tuple header in bytes. Column data starts after it.',
  t_bits: 'The t_bits value marks which columns are null. (none) means no column is null.',
  t_data_hex: 'The t_data_hex value shows the raw column data in hex format.',
  id: 'The id value is the real, decoded value of the id column, read from the live table. (none) means no live row is at this position.',
  payload: 'The payload value is the real, decoded value of the payload column, read from the live table. (none) means no live row is at this position.',
};

const rows = (obj: Record<string, unknown>): [string, string][] =>
  Object.entries(obj).map(([k, v]) => [k, String(v)]);

function rowRects(start: number, end: number) {
  const rects = [];
  let pos = start;
  while (pos < end) {
    const col = pos % BYTES_PER_ROW;
    const n = Math.min(BYTES_PER_ROW - col, end - pos);
    rects.push({ row: Math.floor(pos / BYTES_PER_ROW), col, n });
    pos += n;
  }
  return rects;
}

function Region({ start, end, fill, detail, onEnter }: {
  start: number; end: number; fill: string; detail: Detail; onEnter: (d: Detail) => void;
}) {
  return (
    <g className="region">
      {rowRects(start, end).map((r) => (
        <rect key={`${r.row}-${r.col}`} x={r.col * CELL_W} y={r.row * ROW_H} width={r.n * CELL_W} height={ROW_H - 1}
          fill={fill} tabIndex={0} onMouseEnter={() => onEnter(detail)} onFocus={() => onEnter(detail)}>
          <title>{detail.title}</title>
        </rect>
      ))}
    </g>
  );
}

function ByteMap({ page, onEnter }: { page: HeapPage; onEnter: (d: Detail) => void }) {
  const h = page.header;
  const size = h.page_size || 8192;
  const height = Math.ceil(size / BYTES_PER_ROW) * ROW_H;
  return (
    <svg viewBox={`0 0 ${BYTES_PER_ROW * CELL_W} ${height}`} role="img" aria-label={`Byte map of heap page ${page.block}`}
      style={{ width: '100%', height: 'auto' }}>
      <rect width={BYTES_PER_ROW * CELL_W} height={height} fill={COLORS.free} />
      <Region start={0} end={24} fill={COLORS.header} onEnter={onEnter} detail={{
        title: 'Page header (24 bytes)',
        rows: rows({ lsn: h.lsn, checksum: h.checksum, flags: h.flags, lower: h.lower, upper: h.upper,
          special: h.special, page_size: h.page_size, version: h.version, prune_xid: h.prune_xid }),
      }} />
      {page.items.map((it) => {
        const start = 24 + (it.lp - 1) * 4;
        return <Region key={`p${it.lp}`} start={start} end={start + 4} fill={COLORS.pointer} onEnter={onEnter} detail={{
          title: `Item pointer lp=${it.lp}`,
          rows: rows({ lp: it.lp, lp_off: it.lp_off, lp_flags: it.lp_flags, lp_len: it.lp_len }),
        }} />;
      })}
      <Region start={h.lower} end={h.upper} fill={COLORS.free} onEnter={onEnter} detail={{
        title: `Free space (${page.free_space} bytes)`,
        rows: rows({ lower: h.lower, upper: h.upper, free_space: page.free_space }),
      }} />
      {page.items.map((it, i) => {
        if (!it.lp_len) return null;
        const dead = it.lp_flags !== 1 || it.t_xmax !== '0';
        return <Region key={`t${it.lp}`} start={it.lp_off} end={it.lp_off + it.lp_len}
          fill={dead ? COLORS.dead : i % 2 === 0 ? COLORS.tupleA : COLORS.tupleB} onEnter={onEnter} detail={{
            title: `Tuple lp=${it.lp}`,
            rows: rows({ lp: it.lp, lp_off: it.lp_off, lp_len: it.lp_len, lp_flags: it.lp_flags,
              t_xmin: it.t_xmin, t_xmax: it.t_xmax, t_ctid: it.t_ctid, t_infomask: it.t_infomask,
              t_infomask2: it.t_infomask2, t_hoff: it.t_hoff, t_bits: it.t_bits || '(none)',
              t_data_hex: it.t_data_hex || '(none)', id: it.id ?? '(none)', payload: it.payload ?? '(none)' }),
          }} />;
      })}
      <Region start={h.special} end={size} fill={COLORS.free} onEnter={onEnter} detail={{
        title: 'Special space (0 bytes for heap pages)', rows: rows({ special: h.special, page_size: size }),
      }} />
    </svg>
  );
}

export function KVTable({ rows }: { rows: [string, string][] }) {
  return (
    <table className="kv">
      <tbody>
        {rows.map(([k, v]) => (
          <tr key={k}>
            <th title={FIELD_INFO[k]} style={FIELD_INFO[k] ? { cursor: 'help' } : undefined}>{k}</th>
            <td>{v}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

const LEGEND: [string, string | null][] = [
  ['Header', COLORS.header], ['Item pointers', COLORS.pointer], ['Tuples', COLORS.tupleA],
  ['Dead / redirect', COLORS.dead], ['Free space', null],
];

export function HeapView({ data }: { data: HeapData }) {
  const [detail, setDetail] = useState<Detail | null>(null);
  const pages = data.pages;
  const blockLabel = pages.length > 1 ? `blocks ${pages[0].block}–${pages[pages.length - 1].block}` : `block ${pages[0].block}`;
  const total = pages.reduce((n, p) => n + p.items.length, 0);
  return (
    <div className="heap-page">
      <p className="small">{data.relation} · {blockLabel} · {total} item pointers</p>
      <div className="heap-grid">
        <div className="heap-pages">
          {pages.map((page) => (
            <div className="heap-map" key={page.block}>
              <p className="small muted" style={{ margin: '0 0 4px' }}>
                Block {page.block} · {page.items.length} rows · {page.free_space} bytes free
              </p>
              <ByteMap page={page} onEnter={setDetail} />
            </div>
          ))}
        </div>
        <div className="heap-detail">
          <h3 style={{ margin: '0 0 8px' }}>{detail?.title ?? 'Hover a region'}</h3>
          {detail && <KVTable rows={detail.rows} />}
        </div>
      </div>
      <div className="legend small muted">
        {LEGEND.map(([label, color]) => (
          <span key={label}>
            <span className="swatch" style={color ? { background: color } : { border: '1px solid var(--line)' }} />
            {label}
          </span>
        ))}
      </div>
    </div>
  );
}
