WITH year_counts AS (
    SELECT
        strftime('%Y', v.timestamp) AS year,
        p.path AS path,
        COUNT(*) AS releases
    FROM versions v
             JOIN paths p ON p.id = v.path_id
    GROUP BY year, p.path
),
     ranked AS (
         SELECT
             year,
             path,
             releases,
             ROW_NUMBER() OVER (PARTITION BY year ORDER BY releases DESC) AS rn
         FROM year_counts
     )
SELECT
    year,
    rn AS rank,
    path,
    releases
FROM ranked
WHERE rn <= 10
ORDER BY year, releases DESC;
