WITH variations AS
         (SELECT LOWER(path) AS lowercase_path,
                 COUNT(*)    AS variations_count
          FROM paths
          GROUP BY LOWER(path)
          ORDER BY variations_count DESC)
SELECT COUNT(*) AS total_paths, SUM(variations_count) AS total_variations
FROM variations