import { registerView } from './registry.js';
import { renderHeapSection } from './postgres-heap-page.js';

function keyChip(item) {
  const chip = document.createElement('span');
  chip.className = 'index-chip' + (item.dead ? ' dead' : '');
  chip.textContent = item.data_hex ? item.data_hex.slice(0, 8) : '(none)';
  chip.title = `itemoffset=${item.itemoffset} ctid=${item.ctid}` +
    (item.dead ? ' (dead)' : '');
  return chip;
}

function pageBox(page) {
  const box = document.createElement('div');
  box.className = 'index-page';

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

function treeDiagram(indexPages) {
  const wrap = document.createElement('div');
  wrap.className = 'index-tree';

  const byLevel = new Map();
  for (const page of indexPages.pages) {
    if (!byLevel.has(page.level)) byLevel.set(page.level, []);
    byLevel.get(page.level).push(page);
  }
  const levels = [...byLevel.keys()].sort((a, b) => b - a);

  for (const level of levels) {
    const row = document.createElement('div');
    row.className = 'index-level';
    for (const page of byLevel.get(level)) {
      row.append(pageBox(page));
    }
    wrap.append(row);
  }

  if (indexPages.truncated) {
    const note = document.createElement('p');
    note.className = 'small muted';
    note.textContent = 'More index pages exist beyond what is shown here.';
    wrap.append(note);
  }
  return wrap;
}

registerView('postgres.heap_and_index', {
  title: 'Heap page + B-tree index',
  render(container, { snapshot, focus }) {
    const data = snapshot.data;

    const wrap = document.createElement('div');
    wrap.className = 'heap-and-index';

    const heapSection = document.createElement('div');
    renderHeapSection(heapSection, data.heap, focus?.detail?.lp ?? null);

    const indexHeading = document.createElement('h3');
    indexHeading.style.margin = '20px 0 8px';
    indexHeading.textContent = `Index: ${data.index.index_name}`;

    wrap.append(heapSection, indexHeading, treeDiagram(data.index));
    container.replaceChildren(wrap);
  },
});
