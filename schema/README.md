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
