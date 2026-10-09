---
title: Reading a B-tree index
actions: [insert_rows, update_rows, delete_rows, vacuum_full]
---

## What you will see

The heap page article showed where PostgreSQL stores a table's rows. This
one shows the other half: the B-tree index that lets Postgres find a row by
value without scanning every page. The panel below shows both together —
the heap page on top, the index's real pages underneath — read live from
`pageinspect`, the same way as before. Nothing here is simulated.

The index is on `glasshouse_demo.payload`, not `id`. `insert_rows` never
sets `id` (it stays NULL on every new row), so an index on it would never
fill in or split; `payload` gets a random value on every row, so the tree
actually grows as you insert.

Each box below is one index page: its block number, whether it's the root,
an internal page, or a leaf, and its level (0 is the leaf level). On a leaf
page, each chip shows the real, decoded `payload` value that entry points
to, read live from the table by its heap tuple pointer — the actual key the
tree is sorted on, not a stand-in for it. On an internal or root page, a
chip's `ctid` is a downlink to a child block rather than a heap pointer, so
there is nothing to decode there; it shows the raw key bytes instead. Hover
any chip for its position and, for a leaf entry whose row still exists, both
forms together. The first chip on a page is sometimes not a real entry at
all: it can be a copy of the page's upper bound (a "high key") or, on the
leftmost page of a level, an empty "minus infinity" sentinel — neither one
points anywhere.

## Start the lab

This article shares its lab with the heap-page article — the same compose
file, the same database, the same Inspector:

```sh
curl -fsSL https://raw.githubusercontent.com/AliRasoulinejad/Glasshouse/v0.5.0/adapters/postgres/compose.yml | \
  GLASSHOUSE_INSPECTOR_CONTEXT=https://github.com/AliRasoulinejad/Glasshouse.git#v0.5.0:inspector \
  docker compose -f - up -d --build --wait
```

Once it reports ready, the panel at the end of this article connects to it
on `127.0.0.1:8765`, the same as the heap-page article. If that article's
lab is already running, this one is too — no separate start needed.

## Watch it change

Press **insert_rows** a few times. Each press adds 10 rows; watch the
index's leaf pages fill and, eventually, split into two as a page runs out
of room — a new leaf box appears next to its sibling, at the same level.
The very first leaf split also grows the tree a level: the root leaf splits
into two leaves, and a brand-new root is created above them immediately —
it doesn't take several splits to fill up a root.

**update_rows** sets `payload`, the indexed column, so every update is a
non-HOT update: it marks the old heap tuple dead *and* inserts a new index
entry for the new value — watch for a new chip on each press. **delete_rows**
marks heap tuples dead without touching the index; Postgres only cleans up
the index entry once the row is vacuumed. **vacuum_full** rewrites the table
and the index together, so dead entries disappear from both the heap page
above and the index pages below at the same time.

## When you're done

Stop the lab and remove its data with the same command, swapping `up` for
`down -v`:

```sh
curl -fsSL https://raw.githubusercontent.com/AliRasoulinejad/Glasshouse/v0.5.0/adapters/postgres/compose.yml | \
  GLASSHOUSE_INSPECTOR_CONTEXT=https://github.com/AliRasoulinejad/Glasshouse.git#v0.5.0:inspector \
  docker compose -f - down -v
```

<!-- lab-panel -->
