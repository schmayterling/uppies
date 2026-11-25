alter table instance_images modify kind varchar(32) not null
	check (kind in ('login_background', 'favicon', 'logo'));
