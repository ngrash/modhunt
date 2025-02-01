SELECT paths.*, COUNT(versions.path_id) FROM paths
                                                 JOIN versions ON paths.id = versions.path_id
WHERE LOWER(path) = ?
GROUP BY paths.path
    LIMIT 100;