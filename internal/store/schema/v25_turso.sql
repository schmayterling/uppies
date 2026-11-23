alter table maintenance_windows add column scoped integer not null default 0 check (scoped in (0, 1));

alter table maintenance_windows add column until_further_notice integer not null default 0 check (until_further_notice in (0, 1));

create table if not exists maintenance_window_monitors (
	window_id integer not null references maintenance_windows(id) on delete cascade,
	monitor_id integer not null references monitors(id) on delete cascade,
	primary key (window_id, monitor_id)
) strict;

create index if not exists maintenance_window_monitors_monitor on maintenance_window_monitors(monitor_id);

create table if not exists maintenance_updates (
	id integer primary key,
	window_id integer not null references maintenance_windows(id) on delete cascade,
	body text not null check (length(body) between 1 and 5000),
	at_ms integer not null
) strict;

create index if not exists maintenance_updates_window on maintenance_updates(window_id, id);
