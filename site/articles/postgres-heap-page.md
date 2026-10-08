---
title: Reading a heap page
actions: [insert_rows]
---

## What you will see

PostgreSQL stores a table's rows in fixed-size pages, 8 KiB each. This
article shows you one page of a real table, read from a real running
Postgres with `pageinspect`. Nothing below is simulated: every byte in the
map comes from the database you start in the next step.

## Start the lab

Pull the compose file and start Postgres and the Inspector together.

```sh
curl -fsSL <url>/compose.yml | \
  GLASSHOUSE_INSPECTOR_CONTEXT=<git-url-or-path-to-inspector> \
  docker compose -f - up -d --build --wait
```

Once it reports ready, the panel at the end of this article connects to it
on `127.0.0.1:8765`. The first time, your browser will ask whether this site
may connect to your local network — click **Allow**. That prompt is the
browser confirming you want this page talking to the lab you just started;
without it, the panel cannot reach the Inspector at all.

## Read the page

The panel shows a byte map of one page of the demo table. Hover a region to
see its fields on the right.

- **Item pointers** are small, fixed-size entries just after the header.
  Each one names a tuple by `lp` (its number) and points at it with
  `lp_off` and `lp_len`: the tuple's offset and length in bytes.
- **Tuples** are the row data itself, packed in from the end of the page.
  `t_xmin` is the transaction ID that inserted the tuple. A tuple whose `lp`
  matches the item pointer you are looking at is the one that pointer leads
  to.
- The gap between the item pointers and the tuples is free space: room for
  more rows before the page fills up.

## Insert rows

Press **insert_rows** in the panel. It inserts 10 rows into the demo table
through a fixed, constant statement, nothing the browser can change. Watch
the item pointer list grow and the free space shrink.

## Check it yourself

If you have a checkout of the Glasshouse repository, its demo script does
the same before/after comparison from the command line:

```sh
sh adapters/postgres/demo.sh
```

It prints the page before and after the insert, the same way the panel
shows it. This step is optional — the panel above already shows the same
thing, and a checkout is not required to follow this article.

## When you're done

Stop the lab and remove its data with the same compose file:

```sh
curl -fsSL <url>/compose.yml | \
  GLASSHOUSE_INSPECTOR_CONTEXT=<git-url-or-path-to-inspector> \
  docker compose -f - down -v
```

<!-- lab-panel -->
