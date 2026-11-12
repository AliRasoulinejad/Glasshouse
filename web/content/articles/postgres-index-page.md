---
title: Reading a B-tree index
actions: [insert_rows, update_rows, delete_rows, vacuum, vacuum_full]
steps:
  - id: fresh
    section: What you will see
    actions: []
  - id: split
    section: Watch the tree grow
    actions: ["insert_rows*20"]
  - id: updated
    section: Updates add index entries
    actions: [update_rows]
  - id: vacuumed
    section: Deletes and vacuum
    actions: [delete_rows, vacuum_full]
---

## What you will see

The heap page article showed where PostgreSQL stores a table's rows. This
one shows the B-tree index that lets Postgres find a row by value without
reading every page. The panel shows both: the heap pages on top and the
index's real pages underneath, read from `pageinspect` on a real Postgres 17.
Each state was captured from a fresh database by running the buttons this
article names, in order.

The index is on `glasshouse_demo.payload`. `insert_rows` gives every new row
a random payload, so new keys land all over the tree, and the tree has to
grow to hold them.

## Read an index page

Each box is one index page: its block number, its type (root, internal, or
leaf), and its level (0 is the leaf level). On a fresh table the whole index
is a single page that is both the root and the only leaf.

On a leaf page each chip shows the real decoded `payload` that entry points
to, with the entry's heap pointer (`ctid`) under it. That `ctid` is the
block and line pointer of the row in the heap above.

## Watch the tree grow

Twenty presses of `insert_rows` add 200 rows. That is more than one leaf can
hold. The first time the single leaf fills, it splits into two, and Postgres
creates a new root above them at once; later splits add more leaves under
that same root. Now the tree has two levels: a root at level 1 whose chips
are downlinks to child blocks (their `ctid` names the child page, not a heap
row), and leaves at level 0 that hold the keys in sorted order.

The first chip on a page is sometimes not a real entry: on a leaf that has a
right sibling it is the page's "high key", a copy of its upper bound, and on
the leftmost page of a level above the leaves it is an empty "minus
infinity" item.

## Updates add index entries

`update_rows` changes `payload`, the indexed column, on 3 rows. So each
update is not a HOT update: the old heap tuple becomes dead *and* a new index
entry is added for the new value. Look for three more chips; the old entries
are still there, pointing at the dead tuples.

## Deletes and vacuum

`delete_rows` marks 3 heap tuples dead without touching the index at all.
Then `vacuum_full` rewrites the table and rebuilds the index together, so the
dead entries are gone from both the heap pages and the index pages.
