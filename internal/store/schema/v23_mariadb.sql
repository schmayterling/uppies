alter table projects add column if not exists navbar_link text not null default 'status'
	check (navbar_link in ('status', 'list', 'homepage'));
