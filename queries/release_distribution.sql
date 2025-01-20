SELECT number_of_releases, COUNT(*) AS paths_count
FROM (
         SELECT path_id, COUNT(*) AS number_of_releases
         FROM versions
         GROUP BY path_id
     )
GROUP BY number_of_releases
ORDER BY number_of_releases;