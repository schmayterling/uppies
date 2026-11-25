alter table instance_images drop constraint instance_images_kind_check,
	add constraint instance_images_kind_check check (kind in ('login_background', 'favicon', 'logo'));
