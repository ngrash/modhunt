WITH monthly_counts AS (
    SELECT
        strftime('%Y-%m', v.timestamp) AS yyyymm,
        p.path,
        COUNT(*) AS releases_count
    FROM versions v
             JOIN paths p ON p.id = v.path_id
    GROUP BY strftime('%Y-%m', v.timestamp), p.id
)
SELECT m.yyyymm, m.path, m.releases_count
FROM monthly_counts m
         JOIN (
    SELECT yyyymm, MAX(releases_count) AS max_count
    FROM monthly_counts
    GROUP BY yyyymm
) c ON m.yyyymm = c.yyyymm AND m.releases_count = c.max_count
ORDER BY m.yyyymm;