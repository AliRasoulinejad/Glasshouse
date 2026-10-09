import { registerView } from './registry.js';
import { renderHeapSection, correlatedWALRecords, walRecordsSection } from './postgres-heap-page.js';
import { renderRunningQueries } from './running-queries.js';

function keyChip(item) {
  const chip = document.createElement('span');
  chip.className = 'index-chip' + (item.dead ? ' dead' : '');
  chip.textContent = item.value
    ? item.value
    : item.data_hex ? item.data_hex.slice(0, 23) : '(none)';
  chip.title = `itemoffset=${item.itemoffset} ctid=${item.ctid}` +
    (item.value ? ` value=${item.value}` : '') +
    (item.data_hex ? ` data_hex=${item.data_hex}` : '') +
    (item.dead ? ' (dead)' : '');
  return chip;
}

function pageBox(page, elementsByBlock) {
  const box = document.createElement('div');
  box.className = 'index-page';
  elementsByBlock.set(page.block, box);

  const heading = document.createElement('p');
  heading.className = 'small muted';
  heading.style.margin = '0 0 4px';
  heading.textContent = `Block ${page.block} · ${page.type} · ` +
    `level ${page.level} · ${page.items.length} items`;
  box.append(heading);

  const list = document.createElement('div');
  list.className = 'index-items';
  for (const item of page.items) {
    list.append(keyChip(item));
  }
  box.append(list);
  return box;
}

// downlinkBlock parses the child block number out of an internal page
// item's ctid, formatted by pageinspect as "(block,offset)" — mirrors
// btreepage.go's downlinkBlock. Returns null if it doesn't parse.
function downlinkBlock(ctid) {
  const match = /^\((\d+),\d+\)$/.exec(ctid ?? '');
  if (!match) return null;
  return Number(match[1]);
}

// treeEdges returns one {parent, child} block-number pair per downlink on
// an internal or root page, skipping any child block the walk didn't
// actually include (it may have been cut off by the page cap).
function treeEdges(indexPages) {
  const known = new Set(indexPages.pages.map((p) => p.block));
  const edges = [];
  for (const page of indexPages.pages) {
    if (page.level === 0) continue;
    for (const item of page.items) {
      const child = downlinkBlock(item.ctid);
      if (child !== null && known.has(child)) {
        edges.push({ parent: page.block, child });
      }
    }
  }
  return edges;
}

function treeDiagram(indexPages) {
  const wrap = document.createElement('div');
  wrap.className = 'index-tree';
  const elementsByBlock = new Map();

  // All pages sit in one row, side by side, in walk order (root first,
  // then each level's children) — the connector lines (drawConnectors)
  // are what show the tree shape, not the boxes' row/column position.
  const row = document.createElement('div');
  row.className = 'index-level';
  for (const page of indexPages.pages) {
    row.append(pageBox(page, elementsByBlock));
  }
  wrap.append(row);

  if (indexPages.truncated) {
    const note = document.createElement('p');
    note.className = 'small muted';
    note.textContent = 'More index pages exist beyond what is shown here.';
    wrap.append(note);
  }

  return { element: wrap, elementsByBlock, edges: treeEdges(indexPages) };
}

// drawConnectors overlays one SVG line per parent/child edge, positioned
// from the boxes' actual rendered rects. Must run only after `treeWrap.element`
// is attached to the live document — box positions aren't known before layout.
const SVG_NS = 'http://www.w3.org/2000/svg';
function drawConnectors(treeWrap) {
  if (treeWrap.edges.length === 0) return;

  const containerRect = treeWrap.element.getBoundingClientRect();
  const svg = document.createElementNS(SVG_NS, 'svg');
  svg.setAttribute('class', 'index-tree-lines');
  svg.setAttribute('width', String(containerRect.width));
  svg.setAttribute('height', String(containerRect.height));

  for (const { parent, child } of treeWrap.edges) {
    const parentEl = treeWrap.elementsByBlock.get(parent);
    const childEl = treeWrap.elementsByBlock.get(child);
    if (!parentEl || !childEl) continue;
    const p = parentEl.getBoundingClientRect();
    const c = childEl.getBoundingClientRect();

    const line = document.createElementNS(SVG_NS, 'line');
    line.setAttribute('x1', String(p.left + p.width / 2 - containerRect.left));
    line.setAttribute('y1', String(p.bottom - containerRect.top));
    line.setAttribute('x2', String(c.left + c.width / 2 - containerRect.left));
    line.setAttribute('y2', String(c.top - containerRect.top));
    svg.append(line);
  }

  treeWrap.element.prepend(svg);
}

registerView('postgres.heap_and_index', {
  title: 'Heap page + B-tree index',
  render(container, { snapshot, events, focus }) {
    const data = snapshot.data;

    const wrap = document.createElement('div');
    wrap.className = 'heap-and-index';

    const heapSection = document.createElement('div');
    renderHeapSection(heapSection, data.heap, focus?.detail?.lp ?? null);
    const walRecords = correlatedWALRecords(events, focus);
    if (walRecords.length > 0) {
      heapSection.querySelector('.heap-page')?.append(walRecordsSection(walRecords));
    }

    const indexHeading = document.createElement('h3');
    indexHeading.style.margin = '20px 0 8px';
    indexHeading.textContent = `Index: ${data.index.index_name}`;

    const treeWrap = treeDiagram(data.index);
    wrap.append(heapSection, indexHeading, treeWrap.element);
    renderRunningQueries(wrap, data.queries);
    container.replaceChildren(wrap);
    drawConnectors(treeWrap);
  },
});
