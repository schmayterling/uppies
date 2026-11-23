alter table maintenance_windows add column if not exists scoped smallint not null default 0 check (scoped in (0, 1));

alter table maintenance_windows add column if not exists until_further_notice smallint not null default 0 check (until_further_notice in (0, 1));

create table if not exists maintenance_window_monitors (
	window_id bigint not null,
	monitor_id bigint not null,
	primary key (window_id, monitor_id),
	key maintenance_window_monitors_monitor (monitor_id),
	foreign key (window_id) references maintenance_windows(id) on delete cascade,
	foreign key (monitor_id) references monitors(id) on delete cascade
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_nopad_bin;

create table if not exists maintenance_updates (
	id bigint auto_increment primary key,
	window_id bigint not null,
	body text not null check (char_length(body) between 1 and 5000),
	at_ms bigint not null,
	key maintenance_updates_window (window_id, id),
	foreign key (window_id) references maintenance_windows(id) on delete cascade
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_nopad_bin;
