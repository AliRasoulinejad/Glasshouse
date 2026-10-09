// Viewer shell: connection status, timeline, playback, actions, and the
// view host. All data from the API is written with textContent, never
// innerHTML, so a target-supplied string cannot inject markup.
import { viewFor } from './views/registry.js';
import './views/mock-counter.js';
import './views/postgres-heap-page.js';
import './views/postgres-heap-and-index.js';

const HEALTH_INTERVAL_MS = 2000;
const DOWN_NOTICE_AFTER_MS = 6000;
const MAX_TICKS = 500;

const $ = (id) => document.getElementById(id);

const state = {
  connected: false,
  health: null,
  snapshot: null,
  events: [],      // oldest first, deduplicated by id
  seen: new Set(),
  cursor: null,    // index into events, or null when following live
  playing: false,
  playTimer: null,
  lastOk: 0,
  refreshTimer: null,
  source: null,
};

// ---- connection -----------------------------------------------------------

async function checkHealth() {
  try {
    const res = await fetch('/health', { cache: 'no-store' });
    if (!res.ok) throw new Error(String(res.status));
    const health = await res.json();
    state.lastOk = Date.now();
    if (!state.health) {
      state.health = health;
      onFirstHealthy(health);
    }
    setStatus('live', 'Live');
    hideNotice();
  } catch {
    if (state.health) {
      state.health = null;
      teardownStream();
    }
    if (Date.now() - state.lastOk > DOWN_NOTICE_AFTER_MS) {
      setStatus('down', 'Not running');
      showNotice('Inspector is not reachable on 127.0.0.1. Start it, then check your terminal for errors.');
    } else {
      setStatus('checking', 'Checking…');
    }
  }
}

function onFirstHealthy(health) {
  const name = document.createElement('span');
  name.className = 'target-name';
  name.textContent = health.target;
  const schema = document.createElement('span');
  schema.className = 'target-schema';
  schema.textContent = `schema v${health.schema_version}`;
  $('target').replaceChildren(name, schema);
  renderActions(health.actions ?? []);
  openStream();
  loadSnapshot();
}

function openStream() {
  if (state.source) return;
  // Same-origin, so no CORS is involved. The browser resumes from the last
  // event id on reconnect, so no client-side bookkeeping is needed.
  const source = new EventSource('/events');
  source.addEventListener('event', (msg) => {
    const ev = JSON.parse(msg.data);
    if (state.seen.has(ev.id)) return;
    state.seen.add(ev.id);
    state.events.push(ev);
    if (state.events.length > MAX_TICKS) {
      const dropped = state.events.splice(0, state.events.length - MAX_TICKS);
      if (state.cursor !== null) state.cursor = Math.max(0, state.cursor - dropped.length);
    }
    if (state.cursor === null) scheduleRefresh();
    renderAll();
  });
  state.source = source;
}

function teardownStream() {
  if (state.source) state.source.close();
  state.source = null;
  renderActions([]);
  stopPlaying();
}

function scheduleRefresh() {
  clearTimeout(state.refreshTimer);
  state.refreshTimer = setTimeout(loadSnapshot, 200);
}

async function loadSnapshot() {
  try {
    const res = await fetch('/snapshot', { cache: 'no-store' });
    if (!res.ok) return;
    const env = await res.json();
    state.snapshot = env.snapshot;
    // Merge history the viewer has not seen yet, e.g. events before it loaded.
    for (const ev of env.events ?? []) {
      if (state.seen.has(ev.id)) continue;
      state.seen.add(ev.id);
      state.events.push(ev);
    }
    state.events.sort((a, b) => a.seq - b.seq);
    if (state.events.length > MAX_TICKS) {
      state.events.splice(0, state.events.length - MAX_TICKS);
    }
    renderAll();
  } catch {
    // Health polling reports connection problems; nothing to add here.
  }
}

// ---- status and notices ---------------------------------------------------

function setStatus(stateName, text) {
  $('status').dataset.state = stateName;
  $('status-text').textContent = text;
}

function showNotice(text) {
  const el = $('notice');
  el.textContent = text;
  el.hidden = false;
}

function hideNotice() {
  $('notice').hidden = true;
}

// ---- timeline and playback ------------------------------------------------

function visibleEvents() {
  return state.cursor === null ? state.events : state.events.slice(0, state.cursor + 1);
}

function focusEvent() {
  return state.cursor === null ? null : state.events[state.cursor] ?? null;
}

function renderTimeline() {
  const el = $('timeline');
  el.replaceChildren();
  el.dataset.live = String(state.cursor === null);
  state.events.forEach((ev, i) => {
    const tick = document.createElement('button');
    tick.type = 'button';
    tick.className = 'tick';
    tick.setAttribute('role', 'option');
    tick.setAttribute('aria-selected', String(i === state.cursor));
    tick.title = `${ev.kind} #${ev.seq}`;
    tick.addEventListener('click', () => seek(i));
    el.append(tick);
  });
  if (state.cursor !== null) {
    el.querySelectorAll('.tick')[state.cursor]?.scrollIntoView({ block: 'nearest', inline: 'nearest' });
  } else {
    el.scrollLeft = el.scrollWidth;
  }

  const atEnd = state.cursor === null || state.cursor >= state.events.length - 1;
  $('step-fwd').disabled = atEnd;
  $('step-back').disabled = state.events.length === 0 || state.cursor === 0;
  $('play').disabled = state.events.length === 0;
  $('play').textContent = state.playing ? '❚❚' : '▶';
  $('go-live').disabled = state.cursor === null;
  $('cursor-text').textContent = state.cursor === null
    ? 'Following live.'
    : `Paused at event ${state.cursor + 1} of ${state.events.length}.`;
}

function renderDetail() {
  const ev = focusEvent();
  $('detail').textContent = ev ? JSON.stringify(ev, null, 2) : 'Select an event on the timeline.';
}

function seek(index) {
  state.cursor = Math.max(0, Math.min(index, state.events.length - 1));
  renderAll();
}

function stepBy(delta) {
  stopPlaying();
  if (state.events.length === 0) return;
  const base = state.cursor ?? state.events.length - 1;
  const next = base + delta;
  if (next >= state.events.length - 1) {
    state.cursor = null; // stepping past the end rejoins live
  } else {
    state.cursor = Math.max(0, next);
  }
  renderAll();
}

function goLive() {
  stopPlaying();
  state.cursor = null;
  renderAll();
}

function startPlaying() {
  if (state.events.length === 0) return;
  if (state.cursor === null) state.cursor = 0;
  if (state.cursor >= state.events.length - 1) state.cursor = 0;
  state.playing = true;
  const interval = 1000 / Number($('speed').value);
  state.playTimer = setInterval(() => {
    if (state.cursor >= state.events.length - 1) {
      stopPlaying();
    } else {
      state.cursor += 1;
    }
    renderAll();
  }, interval);
  renderAll();
}

function stopPlaying() {
  if (state.playTimer) clearInterval(state.playTimer);
  state.playTimer = null;
  state.playing = false;
}

// ---- view host -----------------------------------------------------------

function renderView() {
  const snap = state.snapshot;
  const container = $('view');
  if (!snap) {
    $('view-title').textContent = 'Waiting for a snapshot';
    $('view-type').textContent = '';
    const empty = document.createElement('div');
    empty.className = 'empty-state';
    const icon = document.createElement('span');
    icon.className = 'empty-icon pane-glyph';
    icon.setAttribute('aria-hidden', 'true');
    const text = document.createElement('p');
    text.className = 'muted small';
    text.textContent = "The inspector will show the target's state as soon as it connects.";
    empty.append(icon, text);
    container.replaceChildren(empty);
    return;
  }
  $('view-type').textContent = snap.type;
  const view = viewFor(snap.type);
  if (!view) {
    $('view-title').textContent = 'No view registered';
    const pre = document.createElement('pre');
    pre.className = 'detail';
    pre.textContent = JSON.stringify(snap, null, 2);
    container.replaceChildren(pre);
    return;
  }
  $('view-title').textContent = view.title;
  view.render(container, {
    snapshot: snap,
    events: visibleEvents(),
    focus: focusEvent(),
  });
}

// ---- actions --------------------------------------------------------------

function renderActions(names) {
  const el = $('actions');
  el.replaceChildren();
  for (const name of names) {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.textContent = name.replaceAll('_', ' ');
    btn.addEventListener('click', () => runAction(name, btn));
    el.append(btn);
  }
  if (names.length === 0) {
    const none = document.createElement('span');
    none.className = 'muted small';
    none.textContent = 'No actions available.';
    el.append(none);
  }
}

async function runAction(name, btn) {
  const result = $('action-result');
  btn.disabled = true;
  result.textContent = `Running ${name}…`;
  try {
    // The custom header is what the server checks. A cross-site form cannot
    // set it, so only this page can trigger an action.
    const res = await fetch(`/actions/${encodeURIComponent(name)}`, {
      method: 'POST',
      headers: { 'X-Glasshouse-Action': '1' },
    });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    result.textContent = `${name}: done`;
    scheduleRefresh();
  } catch (err) {
    result.textContent = `${name} failed (${err.message}).`;
  } finally {
    btn.disabled = false;
  }
}

// ---- wiring ---------------------------------------------------------------

function renderAll() {
  renderTimeline();
  renderDetail();
  renderView();
}

$('step-back').addEventListener('click', () => stepBy(-1));
$('step-fwd').addEventListener('click', () => stepBy(1));
$('go-live').addEventListener('click', goLive);
$('play').addEventListener('click', () => (state.playing ? stopPlaying() : startPlaying()));
$('speed').addEventListener('change', () => {
  if (state.playing) {
    stopPlaying();
    startPlaying();
  }
});

renderAll();
checkHealth();
setInterval(checkHealth, HEALTH_INTERVAL_MS);
