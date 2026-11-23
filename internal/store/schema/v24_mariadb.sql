alter table projects add column if not exists announcement text not null default ''
	check (char_length(announcement) <= 1000);

alter table projects add column if not exists announcement_color text not null default ''
	check (announcement_color in ('', 'blue', 'green', 'amber', 'red', 'neutral', 'accent'));
