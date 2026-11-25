-- TursoPG cannot drop a constraint, so the table is rebuilt with the wider check.
create table instance_images_new (
	kind text collate "C" primary key check (kind in ('login_background', 'favicon', 'logo')),
	content_type text collate "C" not null,
	data bytea not null check (length(data) between 1 and 2097152),
	updated_at_ms bigint not null
);

insert into instance_images_new select kind, content_type, data, updated_at_ms from instance_images;

drop table instance_images;

alter table instance_images_new rename to instance_images;
