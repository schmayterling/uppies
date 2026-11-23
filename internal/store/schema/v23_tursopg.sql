-- TursoPG accepts CHECK in CREATE TABLE. On upgrades the app validates this choice.
alter table projects add column navbar_link text not null default 'status';
