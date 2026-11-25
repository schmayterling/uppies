alter table projects add column description text not null default '' check (length(description) <= 2000);

update projects set description = tagline where description = '';
