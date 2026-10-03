#!/bin/sh
# Runs once, on first start of the container, as the superuser.
#
# Creates the demo table with a few rows, and a login role for the Inspector
# that can do exactly what the adapter needs: read the page through
# pageinspect, read and insert into the demo table. Nothing else.
set -eu

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  -v inspector_pw="$GLASSHOUSE_INSPECTOR_PASSWORD" <<'SQL'
CREATE EXTENSION IF NOT EXISTS pageinspect;

CREATE TABLE glasshouse_demo (id int, payload text);
INSERT INTO glasshouse_demo (id, payload)
  SELECT g, md5(g::text) FROM generate_series(1, 3) AS g;

CREATE ROLE glasshouse_inspector LOGIN PASSWORD :'inspector_pw';
GRANT CONNECT ON DATABASE glasshouse TO glasshouse_inspector;
GRANT USAGE ON SCHEMA public TO glasshouse_inspector;
GRANT SELECT, INSERT ON glasshouse_demo TO glasshouse_inspector;

-- pageinspect's raw-page functions refuse anyone who is not a superuser, even
-- with EXECUTE granted. So the Inspector does not get pageinspect directly.
-- It gets two wrappers owned by the superuser (SECURITY DEFINER). Each wrapper
-- is hard-wired to glasshouse_demo and takes only an integer block number.
CREATE FUNCTION glasshouse_page_header(blk int)
RETURNS TABLE (lsn text, checksum int, flags int, lower int, upper int,
               special int, pagesize int, version int, prune_xid text)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, public
AS $f$
  SELECT h.lsn::text, h.checksum::int, h.flags::int, h.lower::int, h.upper::int,
         h.special::int, h.pagesize::int, h.version::int, h.prune_xid::text
  FROM page_header(get_raw_page('glasshouse_demo', blk)) AS h
$f$;

CREATE FUNCTION glasshouse_heap_page_items(blk int)
RETURNS TABLE (lp int, lp_off int, lp_flags int, lp_len int,
               t_xmin text, t_xmax text, t_ctid text,
               t_infomask2 int, t_infomask int, t_hoff int,
               t_bits text, t_data_hex text)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, public
AS $f$
  SELECT i.lp::int, i.lp_off::int, i.lp_flags::int, i.lp_len::int,
         i.t_xmin::text, i.t_xmax::text, i.t_ctid::text,
         i.t_infomask2::int, i.t_infomask::int, i.t_hoff::int,
         COALESCE(i.t_bits, ''),
         COALESCE(encode(substring(i.t_data from 1 for 16), 'hex'), '')
  FROM heap_page_items(get_raw_page('glasshouse_demo', blk)) AS i
$f$;

REVOKE ALL ON FUNCTION glasshouse_page_header(int) FROM PUBLIC;
REVOKE ALL ON FUNCTION glasshouse_heap_page_items(int) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION glasshouse_page_header(int) TO glasshouse_inspector;
GRANT EXECUTE ON FUNCTION glasshouse_heap_page_items(int) TO glasshouse_inspector;
SQL
