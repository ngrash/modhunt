WITH paths_with_domains AS (SELECT id,
                                   substr(path, 1, instr(path, '/') - 1) AS domain,
                                   path FROM paths),
     modules_per_domain
         AS (SELECT CASE WHEN domain = '' THEN path ELSE domain END AS domain,
                    COUNT(id)                                       AS modules
             FROM paths_with_domains
             GROUP BY domain),
     versions_per_domain AS (SELECT CASE
                                        WHEN pd.domain = '' THEN pd.path
                                        ELSE pd.domain END AS domain,
                                    COUNT(*)               AS total_versions,
                                    MIN(v.timestamp)       AS first_release,
                                    MAX(v.timestamp)       AS last_release
                             FROM paths_with_domains pd
                                      JOIN versions v ON pd.id = v.path_id
                             GROUP BY CASE
                                          WHEN pd.domain = '' THEN pd.path
                                          ELSE pd.domain END)
SELECT mpd.domain,
       mpd.modules,
       vpd.total_versions,
       vpd.first_release,
       vpd.last_release
FROM modules_per_domain mpd
         JOIN versions_per_domain vpd USING (domain)
ORDER BY mpd.modules DESC;
