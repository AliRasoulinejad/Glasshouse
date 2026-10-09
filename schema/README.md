# Snapshot / event schema

The contract between adapters, the Inspector, and the viewer (and later the
Website). Schema version **1**. The Go types live in
`inspector/internal/adapter/adapter.go`; keep this document in step with them.

## Envelope

`GET /snapshot` returns:

```json
{
  "schema_version": 1,
  "snapshot": {
    "type": "postgres.heap_page",
    "source": "postgres",
    "seq": 42,
    "timestamp": "2026-10-03T12:00:00Z",
    "data": { }
  },
  "events": [ ]
}
```

- `snapshot.type` selects the view component. Adapter-specific from there.
- `snapshot.seq` is the per-source sequence number this snapshot reflects.
  Events with a higher `seq` from the same source happened after it, so a
  viewer can apply the events after a snapshot without gaps or duplicates.
- `events` is the recent history held by the Inspector (bounded, oldest first),
  so a viewer can render its timeline on load.

## Event

```json
{
  "id": "postgres:42",
  "seq": 42,
  "timestamp": "2026-10-03T12:00:00Z",
  "source": "postgres",
  "kind": "tuple_inserted",
  "correlation_id": "optional, opaque",
  "detail": { }
}
```

- `id` is `<source>:<seq>`: unique, and sortable within a source.
- `kind` is declared by each adapter. The viewer must render an unknown kind
  with a generic fallback rather than failing.
- `correlation_id` is opaque. The viewer only groups by it, never interprets it.
- `detail` is adapter-specific.

## Changes from the handover draft

The handover draft was the starting point. These additions are deliberate:

| Added | Why |
| --- | --- |
| `schema_version` on the envelope | Static samples outlive the code that made them. |
| `seq` on snapshot and event, `id` format fixed | Timestamps cannot order or resume a stream reliably. |
| `source` on snapshot | Multi-node adapters need to say which node a snapshot came from. |
| `context.Context` on the adapter interface | Cancellation and timeouts. |
| Stream returns a separate terminal-error channel | Runtime failures must be reported, not silently closed. |

## Static samples

Each adapter ships a static sample in `samples/` matching this envelope
exactly, with `schema_version` set. The Website reads these with zero setup.

## Adapter-specific shapes

`snapshot.data`'s shape is fixed per `snapshot.type`, documented as Go types
next to each adapter — this table is just a map to them:

| Type | Shape | Defined in |
| --- | --- | --- |
| `postgres.heap_page` | `{relation, pages: [{block, header, free_space, items}]}` | `inspector/internal/adapter/postgres/heappage.go` |
| `postgres.heap_and_index` | `{relation, heap: <postgres.heap_page's data>, index: {index_name, pages: [{block, level, type, items}], truncated}}` | `inspector/internal/adapter/postgres/btreepage.go` |

`index.pages[].items[].data_hex` is Postgres's native `bt_page_items` hex
format: SPACE-SEPARATED byte pairs (e.g. `"78 65 39 32"`), not contiguous
hex like the heap page's `t_data_hex`. The first byte is typically a
constant 1-byte varlena header for a short `text` value; the adapter's
query already skips it before truncating. Item 1 on a page is also
sometimes not a real index entry at all: it can be a "high key" (a copy of
the page's upper bound) or, on the leftmost page of a level, a
"minus infinity" sentinel with empty data — a future UI feature could
detect and label these using `bt_page_stats`, but nothing does yet.

`heap.pages[].items[].id`/`.payload` and `index.pages[].items[].value` are
the real, decoded column values for that tuple location, looked up by
joining the live table on `ctid` (`glasshouse_heap_values` in
`compose.yml`) — not derived from `t_data_hex`/`data_hex` by the adapter
itself. All three are `null` when no live row currently sits at that
location (a dead or unused line pointer). `index[].items[].value` is also
always `null` on an internal or root page (level > 0): there, `ctid`
encodes a downlink to a child block, not a heap pointer, so it is never
looked up.
