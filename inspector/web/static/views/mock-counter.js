import { registerView } from './registry.js';

// View for the mock adapter. It exists to prove the shell end to end: the
// counter value, a sparkline of recent ticks, and the focused event.
const SVG_NS = 'http://www.w3.org/2000/svg';

function sparkline(values) {
  const width = 320;
  const height = 64;
  const svg = document.createElementNS(SVG_NS, 'svg');
  svg.setAttribute('viewBox', `0 0 ${width} ${height}`);
  svg.setAttribute('preserveAspectRatio', 'none');
  svg.setAttribute('role', 'img');
  svg.setAttribute('aria-label', 'Counter over recent events');
  svg.setAttribute('class', 'sparkline');
  svg.style.width = '100%';
  svg.style.height = `${height}px`;

  if (values.length < 2) return svg;

  const min = Math.min(...values);
  const max = Math.max(...values);
  const span = max - min || 1;
  const step = width / (values.length - 1);
  const points = values
    .map((v, i) => `${(i * step).toFixed(1)},${(height - 4 - ((v - min) / span) * (height - 8)).toFixed(1)}`)
    .join(' ');

  const line = document.createElementNS(SVG_NS, 'polyline');
  line.setAttribute('points', points);
  line.setAttribute('fill', 'none');
  line.setAttribute('stroke', 'var(--accent)');
  line.setAttribute('stroke-width', '2');
  svg.append(line);
  return svg;
}

registerView('mock.counter', {
  title: 'Mock counter',
  render(container, { snapshot, events, focus }) {
    const value = snapshot.data?.value ?? '—';
    const values = events
      .map((e) => e.detail?.value)
      .filter((v) => typeof v === 'number');

    const wrap = document.createElement('div');

    const big = document.createElement('div');
    big.style.fontSize = '56px';
    big.style.fontWeight = '600';
    big.style.fontVariantNumeric = 'tabular-nums';
    big.textContent = String(value);

    const label = document.createElement('div');
    label.className = 'muted small';
    label.textContent = `counter value · source ${snapshot.source} · seq ${snapshot.seq}`;

    const focusLine = document.createElement('p');
    focusLine.className = 'small';
    focusLine.textContent = focus
      ? `Viewing ${focus.kind} #${focus.seq} (value ${focus.detail?.value ?? '—'})`
      : 'Following live.';

    wrap.append(big, label, sparkline(values), focusLine);
    container.replaceChildren(wrap);
  },
});
