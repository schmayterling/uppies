alter table projects add column if not exists description text not null default ''
	check (char_length(description) <= 2000);

update projects set description = tagline where description = '';
