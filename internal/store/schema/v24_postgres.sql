alter table projects add column announcement text collate "C" not null default ''
	check (length(announcement) <= 1000);

alter table projects add column announcement_color text collate "C" not null default ''
	check (announcement_color in ('', 'blue', 'green', 'amber', 'red', 'neutral', 'accent'));
