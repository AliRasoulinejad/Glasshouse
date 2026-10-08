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
  svg.style.maxWidth = `${BYTES_PER_ROW * CELL_W * 2}px`;
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

function table(rows) {
  const t = document.createElement('table');
  t.className = 'kv';
  for (const [k, v] of rows) {
    const tr = document.createElement('tr');
    const th = document.createElement('th');
    th.textContent = k;
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
    const page = snapshot.data;
    const focusLP = focus?.detail?.lp ?? null;

    const wrap = document.createElement('div');
    wrap.className = 'heap-page';

    const summary = document.createElement('p');
    summary.className = 'small';
    summary.textContent = `${page.relation} · block ${page.block} · ` +
      `${page.items.length} item pointers · ${page.free_space} bytes free`;

    const grid = document.createElement('div');
    grid.className = 'heap-grid';

    const mapBox = document.createElement('div');
    mapBox.className = 'heap-map';
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

    mapBox.append(buildMap(page, focusLP, onEnter), legend());
    grid.append(mapBox, detail);
    wrap.append(summary, grid);
    container.replaceChildren(wrap);
  },
});
