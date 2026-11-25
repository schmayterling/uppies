-- Fresh Uppies schema, version 25.
create table if not exists projects (
	id bigint auto_increment primary key,
	slug varchar(65) not null unique check (char_length(slug) between 1 and 64),
	name text not null check (char_length(name) between 1 and 120),
	created_at_ms bigint not null,
	tagline text not null default '' check (char_length(tagline) <= 200),
	homepage_url text not null default '' check (char_length(homepage_url) <= 2048),
	accent_hue bigint not null default 200 check (accent_hue between 0 and 360),
	theme text not null default 'auto' check (theme in ('auto', 'light', 'dark')),
	show_face smallint not null default 1 check (show_face in (0, 1)),
	history_days bigint not null default 90 check (history_days in (30, 60, 90)),
	custom_css mediumtext not null default '' check (char_length(custom_css) <= 20000),
	footer_text text not null default '' check (char_length(footer_text) <= 500),
	show_powered_by smallint not null default 1 check (show_powered_by in (0, 1)),
	noindex smallint not null default 0 check (noindex in (0, 1)),
	timezone text not null default 'UTC' check (char_length(timezone) between 1 and 64),
	domain varchar(254) not null default '' check (char_length(domain) <= 253),
	surface text not null default 'neutral' check (surface in ('neutral', 'paper')),
	corners text not null default 'soft' check (corners in ('sharp', 'soft', 'round')),
	font text not null default 'system' check (font in ('system', 'rounded', 'serif', 'mono')),
	lookback_days bigint not null default 90 check (lookback_days between 7 and 365),
	backfill text not null default 'none' check (backfill in ('none', 'up', 'degraded', 'down')),
	nav_links text not null default '' check (char_length(nav_links) <= 2000),
	hide_quiet_days smallint not null default 0 check (hide_quiet_days in (0, 1)),
	graph_fill text not null default '' check (graph_fill in ('', 'gradient', 'filled', 'none')),
	position bigint not null default 0,
	navbar_brand text not null default 'icon-name' check (navbar_brand in ('icon', 'icon-name', 'name')),
	navbar_link text not null default 'status' check (navbar_link in ('status', 'list', 'homepage')),
	announcement text not null default '' check (char_length(announcement) <= 1000),
	announcement_color text not null default '' check (announcement_color in ('', 'blue', 'green', 'amber', 'red', 'neutral', 'accent')),
	-- Empty domains do not participate in the unique index.
	unique_domain varchar(254) as (nullif(domain, '')) virtual invisible,
	unique key projects_domain (unique_domain),
	key projects_domain_lookup (domain)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_nopad_bin;

create table if not exists monitors (
	id bigint auto_increment primary key,
	name text not null check (char_length(name) between 1 and 120),
	kind text not null check (kind in ('http', 'tcp', 'dns', 'ping', 'push')),
	target text not null check (char_length(target) between 1 and 2048),
	keyword longtext not null default '',
	interval_s bigint not null check (interval_s between 20 and 86400),
	timeout_ms bigint not null check (timeout_ms between 100 and 60000),
	retries bigint not null check (retries between 0 and 10),
	retry_interval_s bigint not null check (retry_interval_s between 1 and 3600),
	recovery_checks bigint not null check (recovery_checks between 1 and 10),
	paused smallint not null default 0 check (paused in (0, 1)),
	status text not null default 'unknown' check (status in ('unknown', 'up', 'down')),
	status_since_ms bigint not null,
	fail_streak bigint not null default 0,
	ok_streak bigint not null default 0,
	failing_since_ms bigint not null default 0,
	last_check_at_ms bigint not null default 0,
	last_latency_ms bigint not null default 0,
	last_error longtext not null default '',
	created_at_ms bigint not null,
	push_token longtext not null default '',
	last_push_at_ms bigint not null default 0,
	cert_expires_at_ms bigint not null default 0,
	cert_warned_days bigint not null default 0,
	position bigint not null default 0,
	tags text not null default '' check (char_length(tags) <= 1000),
	auto_create_incident text not null default 'draft' check (auto_create_incident in ('off', 'draft', 'publish')),
	auto_resolve_incident text not null default 'drafts' check (auto_resolve_incident in ('off', 'drafts', 'all')),
	-- Empty push tokens do not participate in the unique index.
	unique_push_token longtext as (nullif(push_token, '')) virtual invisible,
	unique key monitors_push_token (unique_push_token),
	key monitors_push_token_lookup (push_token(64))
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_nopad_bin;

create table if not exists checks (
	id bigint auto_increment primary key,
	monitor_id bigint not null,
	at_ms bigint not null,
	ok smallint not null check (ok in (0, 1)),
	latency_ms bigint not null,
	error longtext not null default '',
	location longtext not null default 'local',
	key checks_monitor_at (monitor_id, at_ms),
	key checks_at (at_ms),
	foreign key (monitor_id) references monitors(id) on delete cascade
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_nopad_bin;

create table if not exists daily_rollups (
	monitor_id bigint not null,
	day_start_ms bigint not null,
	checks bigint not null,
	failures bigint not null,
	covered_ms bigint not null,
	down_ms bigint not null,
	latency_sum_ms bigint not null,
	latency_max_ms bigint not null,
	primary key (monitor_id, day_start_ms),
	foreign key (monitor_id) references monitors(id) on delete cascade
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_nopad_bin;

create table if not exists incidents (
	id bigint auto_increment primary key,
	project_id bigint not null,
	title text not null check (char_length(title) between 1 and 200),
	impact text not null check (impact in ('minor', 'major', 'critical')),
	status text not null check (status in ('investigating', 'identified', 'monitoring', 'resolved')),
	published smallint not null default 0 check (published in (0, 1)),
	started_at_ms bigint not null,
	resolved_at_ms bigint,
	created_at_ms bigint not null,
	source_monitor_id bigint,
	key incidents_project_started (project_id, started_at_ms),
	key incidents_source_monitor (source_monitor_id),
	foreign key (project_id) references projects(id) on delete cascade,
	foreign key (source_monitor_id) references monitors(id) on delete set null
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_nopad_bin;

create table if not exists incident_updates (
	id bigint auto_increment primary key,
	incident_id bigint not null,
	status text not null check (status in ('investigating', 'identified', 'monitoring', 'resolved')),
	body text not null check (char_length(body) between 1 and 5000),
	at_ms bigint not null,
	title text not null default '',
	key incident_updates_incident (incident_id, at_ms),
	foreign key (incident_id) references incidents(id) on delete cascade
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_nopad_bin;

create table if not exists incident_monitors (
	incident_id bigint not null,
	monitor_id bigint not null,
	primary key (incident_id, monitor_id),
	key incident_monitors_monitor (monitor_id),
	foreign key (incident_id) references incidents(id) on delete cascade,
	foreign key (monitor_id) references monitors(id) on delete cascade
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_nopad_bin;

create table if not exists events (
	id bigint auto_increment primary key,
	project_id bigint,
	kind text not null check (kind in ('monitor_down', 'monitor_up', 'incident_published', 'incident_update')),
	at_ms bigint not null,
	monitor_id bigint,
	incident_id bigint,
	title longtext not null,
	detail longtext not null default '',
	key events_project (project_id, id),
	key events_monitor (monitor_id),
	key events_incident (incident_id),
	foreign key (project_id) references projects(id) on delete cascade,
	foreign key (monitor_id) references monitors(id) on delete set null,
	foreign key (incident_id) references incidents(id) on delete set null
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_nopad_bin;

create table if not exists users (
	id bigint auto_increment primary key,
	username varchar(65) not null unique check (char_length(username) between 1 and 64),
	password_hash longtext not null,
	created_at_ms bigint not null
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_nopad_bin;

create table if not exists sessions (
	token_hash varchar(191) primary key,
	user_id bigint not null,
	expires_at_ms bigint not null,
	key sessions_user (user_id),
	foreign key (user_id) references users(id) on delete cascade
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_nopad_bin;

create table if not exists project_images (
	id bigint auto_increment primary key,
	project_id bigint not null,
	kind varchar(32) not null check (kind in ('logo', 'favicon')),
	content_type longtext not null,
	data mediumblob not null check (octet_length(data) between 1 and 524288),
	updated_at_ms bigint not null,
	unique (project_id, kind),
	foreign key (project_id) references projects(id) on delete cascade
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_nopad_bin;

create table if not exists maintenance_windows (
	id bigint auto_increment primary key,
	project_id bigint not null,
	title text not null check (char_length(title) between 1 and 200),
	starts_at_ms bigint not null,
	ends_at_ms bigint not null check (ends_at_ms > starts_at_ms),
	created_at_ms bigint not null,
	scoped smallint not null default 0 check (scoped in (0, 1)),
	until_further_notice smallint not null default 0 check (until_further_notice in (0, 1)),
	key maintenance_windows_project (project_id, ends_at_ms),
	foreign key (project_id) references projects(id) on delete cascade
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_nopad_bin;

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

create table if not exists instance_settings (
	`key` varchar(191) primary key,
	value longtext not null
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_nopad_bin;

create table if not exists instance_images (
	kind varchar(32) primary key check (kind in ('login_background', 'favicon')),
	content_type longtext not null,
	data mediumblob not null check (octet_length(data) between 1 and 2097152),
	updated_at_ms bigint not null
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_nopad_bin;

create table if not exists page_monitors (
	project_id bigint not null,
	monitor_id bigint not null,
	position bigint not null,
	display text not null default 'bars' check (display in ('bars', 'latency', 'summary', 'minimal')),
	graph_fill text not null default '' check (graph_fill in ('', 'gradient', 'filled', 'none')),
	category text not null default '' check (char_length(category) <= 80),
	exclude_from_status smallint not null default 0 check (exclude_from_status in (0, 1)),
	primary key (project_id, monitor_id),
	key page_monitors_monitor (monitor_id),
	foreign key (project_id) references projects(id) on delete cascade,
	foreign key (monitor_id) references monitors(id) on delete cascade
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_nopad_bin;

create table if not exists alert_channels (
	id bigint auto_increment primary key,
	kind text not null check (kind in ('webhook', 'discord', 'slack', 'email')),
	url text not null check (char_length(url) between 1 and 2048),
	created_at_ms bigint not null
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_nopad_bin;

create table if not exists monitor_channels (
	monitor_id bigint not null,
	channel_id bigint not null,
	primary key (monitor_id, channel_id),
	key monitor_channels_channel (channel_id),
	foreign key (monitor_id) references monitors(id) on delete cascade,
	foreign key (channel_id) references alert_channels(id) on delete cascade
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_nopad_bin;

create table if not exists page_categories (
	project_id bigint not null,
	name varchar(81) not null check (char_length(name) between 1 and 80),
	position bigint not null,
	primary key (project_id, name),
	foreign key (project_id) references projects(id) on delete cascade
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_nopad_bin;

create table if not exists api_tokens (
	id bigint auto_increment primary key,
	user_id bigint not null,
	name text not null check (char_length(name) between 1 and 80),
	token_hash varchar(65) not null unique check (char_length(token_hash) = 64),
	created_at_ms bigint not null,
	expires_at_ms bigint not null check (expires_at_ms > created_at_ms),
	revoked_at_ms bigint,
	key api_tokens_user (user_id),
	foreign key (user_id) references users(id) on delete cascade
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_nopad_bin;

create table if not exists api_token_permissions (
	token_id bigint not null,
	permission varchar(191) not null,
	primary key (token_id, permission),
	foreign key (token_id) references api_tokens(id) on delete cascade
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_nopad_bin;
