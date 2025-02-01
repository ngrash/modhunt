SELECT
    p.path,
    MIN(v.timestamp) AS first_release,
    MAX(v.timestamp) AS last_release,
    (julianday(MAX(v.timestamp)) - julianday(MIN(v.timestamp))) AS lifetime
FROM paths p
         JOIN versions v ON p.id = v.path_id
GROUP BY p.id
ORDER BY lifetime DESC;