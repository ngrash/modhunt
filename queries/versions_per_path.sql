select p.path, count(*) as version_count
from paths p
         join versions v on v.path_id = p.id
group by p.path
order by count(*) desc;