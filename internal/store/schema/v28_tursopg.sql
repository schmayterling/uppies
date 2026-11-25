-- TursoPG does not support adding a column with a CHECK constraint. The app validates the length.
alter table projects add column description text not null default '';

update projects set description = tagline where description = '';
