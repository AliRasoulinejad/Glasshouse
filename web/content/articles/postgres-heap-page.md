---
title: Reading a heap page
actions: [insert_rows, update_rows, delete_rows, vacuum, vacuum_full]
steps:
  - id: fresh
    section: What you will see
    actions: []
  - id: inserted
    section: Insert rows
    actions: [insert_rows]
  - id: spilled
    section: When a page fills up
    actions: ["insert_rows*12"]
  - id: dead
    section: Dead tuples
    actions: [update_rows, delete_rows]
  - id: reclaimed
    section: Reclaiming the space
    actions: [vacuum_full]
---

## What you will see

PostgreSQL stores a table's rows in fixed-size pages, 8 KiB each. The panel
beside this text shows pages of a real table, read from a real running
Postgres 17 with `pageinspect`. Nothing in it is drawn by hand: each state was
captured from a fresh database by running the exact buttons this article
names, in order.

The demo table is `glasshouse_demo(id int, payload text)`. `payload` holds a
random 32-character hex string. It has no meaning; it only gives each row
some bytes to store. What matters here is the layout around that data.

## Read the page

The panel shows a byte map of the page, one small cell per byte, read top to
bottom. Hover a region to see its fields.

- The **page header** is the first 24 bytes. `lower` and `upper` mark where
  the free space starts and ends.
- **Item pointers** are 4-byte entries just after the header. Each one names
  a tuple by `lp` (its number) and points at it with `lp_off` and `lp_len`:
  the tuple's offset and length in bytes.
- **Tuples** are the row data itself, packed in from the end of the page.
  The detail table shows the raw `t_data_hex` bytes and, next to them, the
  real decoded `id` and `payload`, read from the live table at this tuple's
  position.
- The gap between the pointers and the tuples is **free space**.

A fresh table has three rows, so the page has three pointers at the top and
three tuples at the bottom, with almost all of the page free in between.

## Insert rows

`insert_rows` inserts 10 rows through one fixed statement. The pointer list
grows down from the header, the tuples grow up from the end of the page, and
the free space between them shrinks from both sides.

## When a page fills up

Twelve more presses of `insert_rows` add 120 rows, more than one page can
hold. When the last page has no room left, Postgres starts a new one. The
panel now shows both pages side by side, oldest first: the first one full,
the new one with the rows that spilled over.

## Dead tuples

PostgreSQL never overwrites a row in place. `update_rows` writes a new
version of 3 rows; the old versions stay where they are with `t_xmax` set to
the transaction that replaced them. They are dead, but they still take up
space. `delete_rows` does the same to 3 more rows: nothing moves, the tuples
are only marked dead. The grey regions in the map are those dead tuples.

## Reclaiming the space

`vacuum_full` rewrites the table without the dead tuples. Their pointers
disappear, the live rows are packed again, and the free space comes back.
Plain `vacuum` (also a button in your lab below) instead marks dead space
reusable where it is, without rewriting the table. Try both and compare.
