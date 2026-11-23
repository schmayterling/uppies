alter table projects add column navbar_link text collate "C" not null default 'status'
	check (navbar_link in ('status', 'list', 'homepage'));
