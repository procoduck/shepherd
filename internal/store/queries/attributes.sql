-- name: ListDistinctAttributeKeys :many
SELECT DISTINCT key
FROM (
  SELECT jsonb_object_keys(ci.local_attributes) AS key
  FROM collector_instances ci
  JOIN collectors c ON ci.collector_id = c.id
  JOIN clusters cl ON c.cluster_id = cl.id
  WHERE cl.org_id = $1
  UNION VALUES ('cluster'), ('role')
) AS keys
ORDER BY key;

-- name: ListDistinctAttributeValues :many
SELECT DISTINCT val::text AS val
FROM (
  SELECT ci.local_attributes ->> $2::text AS val
  FROM collector_instances ci
  JOIN collectors c ON ci.collector_id = c.id
  JOIN clusters cl ON c.cluster_id = cl.id
  WHERE cl.org_id = $1
    AND ci.local_attributes ? $2::text
  UNION
  SELECT CASE WHEN $2::text = 'cluster' THEN cl2.name ELSE c2.role END AS val
  FROM collectors c2
  JOIN clusters cl2 ON c2.cluster_id = cl2.id
  WHERE cl2.org_id = $1
    AND $2::text IN ('cluster', 'role')
) AS vals
WHERE val IS NOT NULL
ORDER BY val;
