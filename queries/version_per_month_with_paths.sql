SELECT strftime('%Y-%m', timestamp) AS month,
       COUNT(*)                     AS versions_count,
       COUNT(DISTINCT path_id)      AS paths_count
FROM versions
GROUP BY strftime('%Y-%m', timestamp)
ORDER BY month;