EXPLAIN (ANALYZE, BUFFERS)
SELECT id,name,version,updated_at,data FROM objects
WHERE kind='profiles' ORDER BY name,id LIMIT 1000 OFFSET 4000;
EXPLAIN (ANALYZE, BUFFERS)
SELECT id,name,version,updated_at,data FROM objects
WHERE kind='profiles' ORDER BY name,id LIMIT 20000;
