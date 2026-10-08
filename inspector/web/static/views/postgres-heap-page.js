import { registerView } from './registry.js';

// Byte map of one PostgreSQL heap page (8 KiB), drawn from the real
// pageinspect output. The page is laid out in rows of BYTES_PER_ROW bytes,
// top to bottom, so every byte has a fixed place and each region is a set of
// rectangles that can be hovered:
//
//   [0, 24)            page header
//   [24, lower)        item pointers, 4 bytes each, growing down
//   [lower, upper)     free space
//   [upper, special)   tuples, packed from the end of the page upward
//   [special, size)    special space (empty for heap pages)
//
// Tuples are drawn at their lp_off/lp_len. Dead and redirect pointers still
// occupy space, so they are drawn too, in a muted colour. A tuple whose
// t_xmax is set is also dead (an update or delete has superseded it) even
// though its line pointer stays LP_NORMAL until a prune or vacuum runs.

const SVG_NS = 'http://www.w3.org/2000/svg';
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
  focus: '#e0a526',
  border: 'var(--line)',
};

// Returns [start, end) rectangles, one per row the range touches.
function rowRects(start, end) {
  const rects = [];
  let pos = start;
  while (pos < end) {
    const row = Math.floor(pos / BYTES_PER_ROW);
    const col = pos % BYTES_PER_ROW;
    const n = Math.min(BYTES_PER_ROW - col, end - pos);
    rects.push({ row, col, n });
    pos += n;
  }
  return rects;
}

function region(svg, start, end, fill, label, onEnter) {
  const g = document.createElementNS(SVG_NS, 'g');
  g.setAttribute('class', 'region');
  for (const r of rowRects(start, end)) {
    const rect = document.createElementNS(SVG_NS, 'rect');
    rect.setAttribute('x', String(r.col * CELL_W));
    rect.setAttribute('y', String(r.row * ROW_H));
    rect.setAttribute('width', String(r.n * CELL_W));
    rect.setAttribute('height', String(ROW_H - 1));
    rect.setAttribute('fill', fill);
    rect.setAttribute('tabindex', '0');
    rect.addEventListener('mouseenter', () => onEnter(label));
    rect.addEventListener('focus', () => onEnter(label));
    const title = document.createElementNS(SVG_NS, 'title');
    title.textContent = label.title;
    rect.append(title);
    g.append(rect);
  }
  svg.append(g);
}

function describe(title, rows) {
  return { title, rows };
}

function fieldRows(obj) {
  return Object.entries(obj).map(([k, v]) => [k, String(v)]);
}

function buildMap(page, focusLP, onEnter) {
  const size = page.header.page_size || 8192;
  const rows = Math.ceil(size / BYTES_PER_ROW);
  const svg = document.createElementNS(SVG_NS, 'svg');
  svg.setAttribute('viewBox', `0 0 ${BYTES_PER_ROW * CELL_W} ${rows * ROW_H}`);
  svg.setAttribute('role', 'img');
  svg.setAttribute('aria-label', `Byte map of heap page ${page.block}`);
  svg.setAttribute('class', 'bytemap');
  svg.style.width = '100%';
  svg.style.height = 'auto';

  // Background, so the free region reads as empty rather than missing.
  const bg = document.createElementNS(SVG_NS, 'rect');
  bg.setAttribute('width', String(BYTES_PER_ROW * CELL_W));
  bg.setAttribute('height', String(rows * ROW_H));
  bg.setAttribute('fill', COLORS.free);
  svg.append(bg);

  const h = page.header;
  region(svg, 0, 24, COLORS.header, describe('Page header (24 bytes)', fieldRows({
    lsn: h.lsn, checksum: h.checksum, flags: h.flags, lower: h.lower,
    upper: h.upper, special: h.special, page_size: h.page_size,
    version: h.version, prune_xid: h.prune_xid,
  })), onEnter);

  page.items.forEach((it) => {
    // Pointers are numbered from 1 and packed right after the header.
    const start = 24 + (it.lp - 1) * 4;
    region(svg, start, start + 4, COLORS.pointer, describe(`Item pointer lp=${it.lp}`, fieldRows({
      lp: it.lp, lp_off: it.lp_off, lp_flags: it.lp_flags, lp_len: it.lp_len,
    })), onEnter);
  });

  region(svg, h.lower, h.upper, COLORS.free, describe(`Free space (${page.free_space} bytes)`, fieldRows({
    lower: h.lower, upper: h.upper, free_space: page.free_space,
  })), onEnter);

  page.items.forEach((it, i) => {
    if (!it.lp_len) return;
    const start = it.lp_off;
    const end = it.lp_off + it.lp_len;
    const dead = it.lp_flags !== 1 || it.t_xmax !== '0';
    const fill = it.lp === focusLP
      ? COLORS.focus
      : dead ? COLORS.dead : i % 2 === 0 ? COLORS.tupleA : COLORS.tupleB;
    region(svg, start, end, fill, describe(`Tuple lp=${it.lp}`, fieldRows({
      lp: it.lp, lp_off: it.lp_off, lp_len: it.lp_len, lp_flags: it.lp_flags,
      t_xmin: it.t_xmin, t_xmax: it.t_xmax, t_ctid: it.t_ctid,
      t_infomask: it.t_infomask, t_infomask2: it.t_infomask2,
      t_hoff: it.t_hoff, t_bits: it.t_bits || '(none)',
      t_data_hex: it.t_data_hex || '(none)',
    })), onEnter);
  });

  region(svg, h.special, size, COLORS.free, describe('Special space (0 bytes for heap pages)', fieldRows({
    special: h.special, page_size: size,
  })), onEnter);

  return svg;
}

function legend() {
  const el = document.createElement('div');
  el.className = 'small muted';
  el.style.display = 'flex';
  el.style.flexWrap = 'wrap';
  el.style.gap = '12px';
  const items = [
    ['Header', COLORS.header],
    ['Item pointers', COLORS.pointer],
    ['Tuples', COLORS.tupleA],
    ['Dead / redirect', COLORS.dead],
    ['Focused', COLORS.focus],
    ['Free space', null],
  ];
  for (const [label, color] of items) {
    const s = document.createElement('span');
    const sw = document.createElement('span');
    sw.style.display = 'inline-block';
    sw.style.width = '10px';
    sw.style.height = '10px';
    sw.style.marginRight = '4px';
    sw.style.borderRadius = '2px';
    sw.style.background = color ?? 'transparent';
    sw.style.border = color ? 'none' : `1px solid ${COLORS.border}`;
    s.append(sw, label);
    el.append(s);
  }
  return el;
}

// Hover text for field names in the detail table, keyed by field name as it
// appears in fieldRows(). Covers page header, item pointer, and tuple header
// fields from pageinspect's heap_page_items()/page_header().
//
// Wording follows ASD-STE100 (Simplified Technical English): short sentences,
// active voice, one idea per sentence, and the same word for the same thing
// every time (page, row, tuple, pointer). Full definitions are in glossary.html.
const FIELD_INFO = {
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
};

const GLOSSARY_URL = 'glossary.html';

// A JS-rendered tooltip, not the native `title` attribute: the native one
// only appears after the browser's own hover-dwell timer, which synthetic or
// automated pointer events (e.g. a screenshot tool moving the cursor) do not
// reliably trigger. This one shows on the mouseenter/focus event itself.
let tooltipEl = null;
let tooltipHideTimer = null;

function tooltip() {
  if (tooltipEl) return tooltipEl;
  tooltipEl = document.createElement('div');
  tooltipEl.setAttribute('role', 'tooltip');
  Object.assign(tooltipEl.style, {
    position: 'fixed',
    zIndex: '1000',
    maxWidth: '280px',
    padding: '6px 10px',
    borderRadius: '6px',
    border: '1px solid var(--line)',
    background: 'var(--panel)',
    color: 'var(--ink)',
    font: '12px/1.45 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
    boxShadow: '0 2px 8px rgba(0,0,0,.3)',
    display: 'none',
  });
  // The pointer can move from the field name onto the tooltip to reach the
  // glossary link, so cancel the pending hide while it is over either one.
  tooltipEl.addEventListener('mouseenter', cancelHideTooltip);
  tooltipEl.addEventListener('mouseleave', hideTooltip);
  document.body.append(tooltipEl);
  return tooltipEl;
}

function showTooltip(target, text, key) {
  cancelHideTooltip();
  const el = tooltip();
  el.replaceChildren();
  const p = document.createElement('p');
  p.style.margin = '0 0 4px';
  p.textContent = text;
  const a = document.createElement('a');
  a.href = `${GLOSSARY_URL}#${key}`;
  a.target = '_blank';
  a.rel = 'noopener';
  a.textContent = 'Glossary ↗';
  a.style.color = 'var(--accent)';
  el.append(p, a);
  el.style.display = 'block';
  const r = target.getBoundingClientRect();
  let left = r.left;
  el.style.top = `${r.bottom + 6}px`;
  el.style.left = `${left}px`;
  // Keep it on screen once its size is known.
  requestAnimationFrame(() => {
    const tr = el.getBoundingClientRect();
    if (tr.right > window.innerWidth - 8) {
      left = Math.max(8, window.innerWidth - tr.width - 8);
      el.style.left = `${left}px`;
    }
    if (tr.bottom > window.innerHeight - 8) {
      el.style.top = `${r.top - tr.height - 6}px`;
    }
  });
}

function cancelHideTooltip() {
  clearTimeout(tooltipHideTimer);
  tooltipHideTimer = null;
}

function hideTooltip() {
  cancelHideTooltip();
  // A short delay gives the pointer time to reach the tooltip itself.
  tooltipHideTimer = setTimeout(() => {
    if (tooltipEl) tooltipEl.style.display = 'none';
  }, 150);
}

function table(rows) {
  const t = document.createElement('table');
  t.className = 'kv';
  for (const [k, v] of rows) {
    const tr = document.createElement('tr');
    const th = document.createElement('th');
    th.textContent = k;
    const info = FIELD_INFO[k];
    if (info) {
      th.style.cursor = 'help';
      th.tabIndex = 0;
      th.addEventListener('mouseenter', () => showTooltip(th, info, k));
      th.addEventListener('mouseleave', hideTooltip);
      th.addEventListener('focus', () => showTooltip(th, info, k));
      th.addEventListener('blur', hideTooltip);
    }
    const td = document.createElement('td');
    td.textContent = v;
    tr.append(th, td);
    t.append(tr);
  }
  return t;
}

registerView('postgres.heap_page', {
  title: 'Heap page (byte map)',
  render(container, { snapshot, focus }) {
    const data = snapshot.data;
    const pages = data.pages;
    const focusLP = focus?.detail?.lp ?? null;

    const wrap = document.createElement('div');
    wrap.className = 'heap-page';

    const blockLabel = pages.length > 1
      ? `blocks ${pages[0].block}–${pages[pages.length - 1].block}`
      : `block ${pages[0].block}`;
    const totalItems = pages.reduce((n, p) => n + p.items.length, 0);
    const summary = document.createElement('p');
    summary.className = 'small';
    summary.textContent = `${data.relation} · ${blockLabel} · ` +
      `${totalItems} item pointers`;

    const grid = document.createElement('div');
    grid.className = 'heap-grid';

    const pagesBox = document.createElement('div');
    pagesBox.className = 'heap-pages';

    const detail = document.createElement('div');
    detail.className = 'heap-detail';
    const detailTitle = document.createElement('h3');
    detailTitle.textContent = 'Hover a region';
    detailTitle.style.margin = '0 0 8px';
    detail.append(detailTitle);

    const onEnter = (d) => {
      detailTitle.textContent = d.title;
      detail.replaceChildren(detailTitle, table(d.rows));
    };

    for (const page of pages) {
      const mapBox = document.createElement('div');
      mapBox.className = 'heap-map';
      const heading = document.createElement('p');
      heading.className = 'small muted';
      heading.style.margin = '0 0 4px';
      heading.textContent = `Block ${page.block} · ${page.items.length} rows · ` +
        `${page.free_space} bytes free`;
      mapBox.append(heading, buildMap(page, focusLP, onEnter));
      pagesBox.append(mapBox);
    }

    grid.append(pagesBox, detail);
    wrap.append(summary, grid, legend());
    container.replaceChildren(wrap);
  },
});
