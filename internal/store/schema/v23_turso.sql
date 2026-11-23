alter table projects add column navbar_link text not null default 'status'
	check (navbar_link in ('status', 'list', 'homepage'));
