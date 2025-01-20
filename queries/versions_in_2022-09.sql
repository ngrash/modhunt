SELECT p.path,
       COUNT(*) AS release_count
FROM versions v
         JOIN paths p ON p.id = v.path_id
WHERE v.timestamp >= '2022-09-01'
  AND v.timestamp < '2022-10-01'
GROUP BY p.path
ORDER BY release_count DESC;