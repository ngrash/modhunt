SELECT
    strftime('%Y-%m', timestamp) AS year_month,
    COUNT(*) AS versions_count
FROM versions
GROUP BY strftime('%Y-%m', timestamp)
ORDER BY year_month;