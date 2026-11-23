-- TursoPG does not support adding a column with a CHECK constraint.
alter table projects add column announcement text not null default '';

alter table projects add column announcement_color text not null default '';
