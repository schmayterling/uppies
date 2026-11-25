alter table projects add column description text collate "C" not null default '' check (length(description) <= 2000);

update projects set description = tagline where description = '';
