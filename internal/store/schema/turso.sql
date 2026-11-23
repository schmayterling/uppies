-- Fresh Uppies schema, version 20.
create table if not exists projects (
	id integer primary key,
	slug text not null unique check (length(slug) between 1 and 64),
	name text not null check (length(name) between 1 and 120),
	created_at_ms integer not null,
	tagline text not null default '' check (length(tagline) <= 200),
	homepage_url text not null default '' check (length(homepage_url) <= 2048),
	accent_hue integer not null default 200 check (accent_hue between 0 and 360),
	theme text not null default 'auto' check (theme in ('auto', 'light', 'dark')),
	show_face integer not null default 1 check (show_face in (0, 1)),
	history_days integer not null default 90 check (history_days in (30, 60, 90)),
	custom_css text not null default '' check (length(custom_css) <= 20000),
	footer_text text not null default '' check (length(footer_text) <= 500),
	show_powered_by integer not null default 1 check (show_powered_by in (0, 1)),
	noindex integer not null default 0 check (noindex in (0, 1)),
	timezone text not null default 'UTC' check (length(timezone) between 1 and 64),
	domain text not null default '' check (length(domain) <= 253),
	surface text not null default 'neutral' check (surface in ('neutral', 'paper')),
	corners text not null default 'soft' check (corners in ('sharp', 'soft', 'round')),
	font text not null default 'system' check (font in ('system', 'rounded', 'serif', 'mono')),
	lookback_days integer not null default 90 check (lookback_days between 7 and 365),
	backfill text not null default 'none' check (backfill in ('none', 'up', 'degraded', 'down')),
	nav_links text not null default '' check (length(nav_links) <= 2000),
	hide_quiet_days integer not null default 0 check (hide_quiet_days in (0, 1)),
	graph_fill text not null default '' check (graph_fill in ('', 'gradient', 'filled', 'none')),
	position integer not null default 0,
	navbar_brand text not null default 'icon-name' check (navbar_brand in ('icon', 'icon-name', 'name'))
) strict;

create unique index if not exists projects_domain on projects(domain) where domain <> '';

create table if not exists monitors (
	id integer primary key,
	name text not null check (length(name) between 1 and 120),
	kind text not null check (kind in ('http', 'tcp', 'dns', 'ping', 'push')),
	target text not null check (length(target) between 1 and 2048),
	keyword text not null default '',
	interval_s integer not null check (interval_s between 20 and 86400),
	timeout_ms integer not null check (timeout_ms between 100 and 60000),
	retries integer not null check (retries between 0 and 10),
	retry_interval_s integer not null check (retry_interval_s between 1 and 3600),
	recovery_checks integer not null check (recovery_checks between 1 and 10),
	paused integer not null default 0 check (paused in (0, 1)),
	status text not null default 'unknown' check (status in ('unknown', 'up', 'down')),
	status_since_ms integer not null,
	fail_streak integer not null default 0,
	ok_streak integer not null default 0,
	failing_since_ms integer not null default 0,
	last_check_at_ms integer not null default 0,
	last_latency_ms integer not null default 0,
	last_error text not null default '',
	created_at_ms integer not null,
	push_token text not null default '',
	last_push_at_ms integer not null default 0,
	cert_expires_at_ms integer not null default 0,
	cert_warned_days integer not null default 0,
	position integer not null default 0,
	tags text not null default '' check (length(tags) <= 1000)
) strict;

create unique index if not exists monitors_push_token on monitors(push_token) where push_token <> '';

create table if not exists checks (
	id integer primary key,
	monitor_id integer not null references monitors(id) on delete cascade,
	at_ms integer not null,
	ok integer not null check (ok in (0, 1)),
	latency_ms integer not null,
	error text not null default '',
	location text not null default 'local'
) strict;

create index if not exists checks_monitor_at on checks(monitor_id, at_ms);

create index if not exists checks_at on checks(at_ms);

create table if not exists daily_rollups (
	monitor_id integer not null references monitors(id) on delete cascade,
	day_start_ms integer not null,
	checks integer not null,
	failures integer not null,
	covered_ms integer not null,
	down_ms integer not null,
	latency_sum_ms integer not null,
	latency_max_ms integer not null,
	primary key (monitor_id, day_start_ms)
) strict;

create table if not exists incidents (
	id integer primary key,
	project_id integer not null references projects(id) on delete cascade,
	title text not null check (length(title) between 1 and 200),
	impact text not null check (impact in ('minor', 'major', 'critical')),
	status text not null check (status in ('investigating', 'identified', 'monitoring', 'resolved')),
	published integer not null default 0 check (published in (0, 1)),
	started_at_ms integer not null,
	resolved_at_ms integer,
	created_at_ms integer not null
) strict;

create index if not exists incidents_project_started on incidents(project_id, started_at_ms);

create table if not exists incident_updates (
	id integer primary key,
	incident_id integer not null references incidents(id) on delete cascade,
	status text not null check (status in ('investigating', 'identified', 'monitoring', 'resolved')),
	body text not null check (length(body) between 1 and 5000),
	at_ms integer not null
) strict;

create index if not exists incident_updates_incident on incident_updates(incident_id, at_ms);

create table if not exists incident_monitors (
	incident_id integer not null references incidents(id) on delete cascade,
	monitor_id integer not null references monitors(id) on delete cascade,
	primary key (incident_id, monitor_id)
) strict;

create index if not exists incident_monitors_monitor on incident_monitors(monitor_id);

create table if not exists events (
	id integer primary key,
	project_id integer references projects(id) on delete cascade,
	kind text not null check (kind in ('monitor_down', 'monitor_up', 'incident_published', 'incident_update')),
	at_ms integer not null,
	monitor_id integer references monitors(id) on delete set null,
	incident_id integer references incidents(id) on delete set null,
	title text not null,
	detail text not null default ''
) strict;

create index if not exists events_project on events(project_id, id);

create index if not exists events_monitor on events(monitor_id);

create index if not exists events_incident on events(incident_id);

create table if not exists users (
	id integer primary key,
	username text not null unique check (length(username) between 1 and 64),
	password_hash text not null,
	created_at_ms integer not null
) strict;

create table if not exists sessions (
	token_hash text primary key,
	user_id integer not null references users(id) on delete cascade,
	expires_at_ms integer not null
) strict;

create index if not exists sessions_user on sessions(user_id);

create table if not exists project_images (
	id integer primary key,
	project_id integer not null references projects(id) on delete cascade,
	kind text not null check (kind in ('logo', 'favicon')),
	content_type text not null,
	data blob not null check (length(data) between 1 and 524288),
	updated_at_ms integer not null,
	unique (project_id, kind)
) strict;

create table if not exists maintenance_windows (
	id integer primary key,
	project_id integer not null references projects(id) on delete cascade,
	title text not null check (length(title) between 1 and 200),
	starts_at_ms integer not null,
	ends_at_ms integer not null check (ends_at_ms > starts_at_ms),
	created_at_ms integer not null
) strict;

create index if not exists maintenance_windows_project on maintenance_windows(project_id, ends_at_ms);

create table if not exists instance_settings (
	key text primary key,
	value text not null
) strict;

create table if not exists instance_images (
	kind text primary key check (kind in ('login_background', 'favicon')),
	content_type text not null,
	data blob not null check (length(data) between 1 and 2097152),
	updated_at_ms integer not null
) strict;

create table if not exists page_monitors (
	project_id integer not null references projects(id) on delete cascade,
	monitor_id integer not null references monitors(id) on delete cascade,
	position integer not null,
	display text not null default 'bars' check (display in ('bars', 'latency', 'summary', 'minimal')),
	graph_fill text not null default '' check (graph_fill in ('', 'gradient', 'filled', 'none')),
	category text not null default '' check (length(category) <= 80),
	primary key (project_id, monitor_id)
) strict;

create index if not exists page_monitors_monitor on page_monitors(monitor_id);

create table if not exists alert_channels (
	id integer primary key,
	kind text not null check (kind in ('webhook', 'discord', 'slack', 'email')),
	url text not null check (length(url) between 1 and 2048),
	created_at_ms integer not null
) strict;

create table if not exists monitor_channels (
	monitor_id integer not null references monitors(id) on delete cascade,
	channel_id integer not null references alert_channels(id) on delete cascade,
	primary key (monitor_id, channel_id)
) strict;

create index if not exists monitor_channels_channel on monitor_channels(channel_id);

create table if not exists page_categories (
	project_id integer not null references projects(id) on delete cascade,
	name text not null check (length(name) between 1 and 80),
	position integer not null,
	primary key (project_id, name)
) strict;

create table if not exists api_tokens (
	id integer primary key autoincrement,
	user_id integer not null references users(id) on delete cascade,
	name text not null check (length(name) between 1 and 80),
	token_hash text not null unique check (length(token_hash) = 64),
	created_at_ms integer not null,
	expires_at_ms integer not null check (expires_at_ms > created_at_ms),
	revoked_at_ms integer
) strict;

create index if not exists api_tokens_user on api_tokens(user_id);

create table if not exists api_token_permissions (
	token_id integer not null references api_tokens(id) on delete cascade,
	permission text not null,
	primary key (token_id, permission)
) strict;
