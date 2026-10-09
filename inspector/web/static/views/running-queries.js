// Shared "running queries" section, rendered by both postgres.heap_page
// and postgres.heap_and_index views from their snapshot's queries array.
// Not registered with registry.js itself: it has no snapshot.type of its
// own, since it is a sibling section on two existing types, not a new one.

const MAX_QUERY_TEXT = 140;

function truncate(text) {
  if (!text) return '';
  return text.length > MAX_QUERY_TEXT ? `${text.slice(0, MAX_QUERY_TEXT)}…` : text;
}

function cell(tag, text) {
  const el = document.createElement(tag);
  el.textContent = text;
  return el;
}

// renderRunningQueries appends a running-queries table into container. It
// does not clear container's existing children; callers pass a dedicated
// child element so placement within a larger view stays under their
// control (see postgres-heap-page.js and postgres-heap-and-index.js).
export function renderRunningQueries(container, queries) {
  queries = queries ?? [];
  const heading = document.createElement('h3');
  heading.style.margin = '20px 0 8px';
  heading.textContent = `Running queries (${queries.length})`;
  container.append(heading);

  if (queries.length === 0) {
    const empty = document.createElement('p');
    empty.className = 'small muted';
    empty.textContent = 'No active backends right now.';
    container.append(empty);
    return;
  }

  const table = document.createElement('table');
  table.className = 'queries';

  const head = document.createElement('tr');
  for (const label of ['PID', 'State', 'Wait event', 'Duration', 'Query']) {
    head.append(cell('th', label));
  }
  table.append(head);

  for (const q of queries) {
    const row = document.createElement('tr');
    const waitEvent = q.wait_event_type ? `${q.wait_event_type}: ${q.wait_event}` : '';
    row.append(
      cell('td', String(q.pid)),
      cell('td', q.state),
      cell('td', waitEvent),
      cell('td', `${q.duration_ms} ms`),
      cell('td', truncate(q.query)),
    );
    table.append(row);
  }

  container.append(table);
}
