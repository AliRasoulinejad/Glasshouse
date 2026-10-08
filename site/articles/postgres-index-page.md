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
an internal page, or a leaf, and its level (0 is the leaf level). The chips
inside are key prefixes — hover one for its exact position and the heap
tuple (or child page) it points to.

## Start the lab

This article shares its compose file with the heap-page article, switched
to the index adapter:

```sh
curl -fsSL https://raw.githubusercontent.com/AliRasoulinejad/Glasshouse/v0.1.0/adapters/postgres/compose.yml | \
  GLASSHOUSE_INSPECTOR_CONTEXT=https://github.com/AliRasoulinejad/Glasshouse.git#v0.1.0:inspector \
  GLASSHOUSE_ADAPTER=postgres-index \
  docker compose -f - up -d --build --wait
```

Once it reports ready, the panel at the end of this article connects to it
on `127.0.0.1:8765`, the same as the heap-page article. Only one of the two
labs can run on that port at a time — stop this one with `down -v` before
starting the other.

## Watch it change

Press **insert_rows** a few times. Each press adds 10 rows; watch the
index's leaf pages fill and, eventually, split into two as a page runs out
of room — a new leaf box appears below, and the tree grows a level once a
single root page can no longer hold all the leaf pointers.

**update_rows** and **delete_rows** mark heap tuples dead without touching
the index's keys directly — Postgres only cleans up the index entry once
the row is vacuumed. **vacuum_full** rewrites the table and the index
together, so dead entries disappear from both the heap page above and the
index pages below at the same time.

## When you're done

Stop the lab and remove its data with the same command, swapping `up` for
`down -v`:

```sh
curl -fsSL https://raw.githubusercontent.com/AliRasoulinejad/Glasshouse/v0.1.0/adapters/postgres/compose.yml | \
  GLASSHOUSE_INSPECTOR_CONTEXT=https://github.com/AliRasoulinejad/Glasshouse.git#v0.1.0:inspector \
  GLASSHOUSE_ADAPTER=postgres-index \
  docker compose -f - down -v
```

<!-- lab-panel -->
