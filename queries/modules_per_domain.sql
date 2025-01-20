WITH paths_with_domains AS
         (SELECT id,
                 substr(path, 1, instr(path, '/') - 1) AS domain,
                 path
          FROM paths),
     modules_per_domain AS
         (SELECT CASE
                     WHEN domain = '' THEN path
                     ELSE domain
                     END   AS domain,
                 COUNT(id) AS modules
          FROM paths_with_domains
          GROUP BY domain
          ORDER BY modules DESC)
SELECT *
FROM modules_per_domain