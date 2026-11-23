# Progress

This file is the state of the project for whoever picks it up next.
Dates are ISO dates in +08:00 unless a time says otherwise.

## Goal

uppies is a self-hosted uptime monitor: one Go binary, one SQLite file,
server-rendered pages. Version 1 covers monitors, incident management, a
timeline view and public status pages for several projects from one install.

## Check that everything still works

```sh
go build ./... && go vet ./... && go test -race ./...
```

Last run: 2026-11-23, all six packages passed, after the basics were finished.

## Done and verified

Each line names how it was verified.

- State machine for monitors (`internal/health`): retries before down, good checks before up, downtime accounting. Verified by table tests, each seen failing once.
- SQLite store (`internal/store`): schema version 1, projects, monitors, checks, daily rollups, events, incidents, users, sessions. Verified by tests on a temporary database file.
- HTTP and TCP checks (`internal/check`). Verified by tests against local servers.
- Check engine (`internal/engine`): one goroutine per monitor, at most 32 checks at once, reload on change, raw checks pruned after 48 hours. Verified by tests for one check cycle. The scheduling loop itself and pruning are verified only by the manual run below.
- Web layer (`internal/web`): status pages, first-run setup, sign-in with a rate limit, projects, monitors, incidents, timeline. Verified by handler tests for the access rules and by the manual run below.
- Manual end-to-end run on 2026-11-22 with the real binary and a local target: setup, refused requests (303 without a session, 403 cross-site, 422 invalid input, 429 after five failed sign-ins), an outage confirmed 22 seconds after the target stopped (interval 20 s, 2 retries 1 s apart), recovery confirmed 40 seconds after it came back, the draft incident opened and resolved, and a clean exit on SIGTERM.
- Alerts (`internal/alert`, sent from `internal/engine`): a plain webhook with JSON, Discord and Slack, on confirmed down and up, with up to three attempts for network errors and 429 or 5xx answers. Verified by tests against local receivers and by a manual run on 2026-11-23 in which a real outage and recovery each produced one message per channel.
- Outside heartbeat (`-heartbeat-url`): one request a minute to a URL you choose. Verified by a test and by the manual run.
- Schema version 2: status page settings on projects, a `public` flag on monitors, and the `alert_channels` table. Verified by a test that upgrades a version 1 file and by starting the new binary on the version 1 demo database (2 projects and 201 rollups kept).
- Project settings page: alert channels (add, remove, send test) and status page settings (name, tagline, homepage, theme, accent hue, days of history, face, custom CSS). Verified by handler tests and the manual run. The themed status page was captured once at 1280 wide.
- Schema version 3 and the settings that use it: footer text, a switch for the "powered by" line, a switch that asks search engines not to list the page, a timezone for incident times, and a custom domain whose front page is the status page. Verified by store and handler tests, and by a manual run on 2026-11-23 on the demo database (upgraded from version 2, 2 projects kept).
- Logo and favicon uploads (schema version 4, `internal/web/images.go`): up to 512 KB each, the type is read from the file's bytes, and images are served sandboxed so that a script inside an SVG cannot run. Verified by store and handler tests, and by uploading an SVG to the demo database with the real binary on 2026-11-23 (a text file was refused with 422).
- Status badge at `/s/<address>/badge.svg`. Verified by a handler test and by rendering it once in a browser.
- Schema version 5 (`internal/store/store.go`): the monitors and alert channel tables are rebuilt to allow new kinds, and maintenance windows are added. Migrations now run with foreign keys off on one connection, check foreign keys before committing, and write a backup file (`<database>.backup-v<old version>`) before any upgrade. Verified by an upgrade test that checks rows pointing at monitors survive, and by upgrading the demo database (5 monitors and 206 rollups kept, integrity and foreign key checks clean).
- DNS and ping checks (`internal/check`). Verified by tests against localhost and reserved addresses, and by the manual run. Ping covers IPv4 only.
- Push monitors: the job requests `/push/<token>`, and silence longer than one interval plus 10 seconds fails the check. Verified by tests and by the manual run (down after 44 seconds of silence at a 20-second interval).
- Certificate expiry: read from each HTTPS check, shown in the admin table, with one alert at 14, 7, 3 and 1 days. The reading and the threshold logic are verified by tests. No real certificate near expiry was tried.
- Email alerts over SMTP (`internal/alert/email.go`, flags `-smtp-addr` and `-smtp-from`). Verified by a test against a fake server and by the manual run against a local fake server. Not tried against a real mail server, so STARTTLS and sign-in are untested.
- Maintenance windows per project: no checks during a window, announced on the status page. Verified by engine and handler tests and by the manual run.
- Dockerfile: written and not built, because the Docker daemon on this machine was not running. The same static Linux build command it uses was run by hand and produced a 15 MB binary.
- Status page rendered and measured at 1280 and 390 CSS pixels wide in the light theme: no horizontal overflow, 90 day cells on wide screens and 30 on narrow ones. The dark theme was looked at once before the last style fixes and not measured.

## Done but not verified

- Colour contrast of the status colours on each surface was not measured. The values come unchanged from the token file.
- Keyboard-only use and screen reader output were not checked.
- Behaviour with many monitors (hundreds) was not measured. There are no memory or speed figures yet.
- The admin pages were captured once at 1280 wide before the last style fixes and not again. The settings page was never captured.
- Discord and Slack alerts were checked against a local receiver only, never against the real services. The field names come from their documentation.

## Decisions

- 2026-11-22: Go, embedded SQLite through `modernc.org/sqlite` (no cgo), server-rendered pages. Chosen by the user. Rejected: TypeScript on Bun or Node.js, Rust, a single-page app.
- 2026-11-22: SQLite stays the default database. PostgreSQL is the only planned second backend. Turso and KineDB were considered and set aside: Turso's embedded engine is not at 1.0, Turso Cloud would add a network service, and KineDB is not publicly available.
- 2026-11-22: One goroutine per monitor in place of a shared time-ordered queue. It is simpler, and the expected size is 5 to 200 monitors.
- 2026-11-22: Only daily rollups are stored. Hourly rollups wait until a view needs them, and adding them is an additive migration.
- 2026-11-22: Rollups keep the latency sum and maximum, not a p95, because a p95 cannot be added up from partial results.
- 2026-11-22: A draft incident never contains the raw check error. That text can name private hosts and becomes public when the draft is published.
- 2026-11-22: The public status page shows a monitor's name and state only, never its target or error text.
- 2026-11-22: A day with under one hour of downtime is drawn as a disruption, a day with one hour or more as an outage. This threshold is a judgment call and has no source.
- 2026-11-22: Cross-site form posts are refused with `http.CrossOriginProtection` from the standard library in place of CSRF tokens.

- 2026-11-23: Alerts go out as webhooks only (plain JSON, Discord, Slack). Email is postponed because SMTP needs server settings, credentials and TLS choices.
- 2026-11-23: uppies does not hold back alerts when every monitor fails at once. Without an independent reference it could not tell its own network loss from a real total outage, and staying silent in a real outage is the worse error. The outside heartbeat covers the case where uppies itself is down.
- 2026-11-23: A project's look is served as its own stylesheet at `/s/<address>/theme.css`. This keeps the content security policy free of inline styles. The accent setting is a hue only: lightness and chroma stay fixed so contrast holds in both themes.
- 2026-11-23: Custom CSS is accepted as written. Only the signed-in admin can set it, and CSS cannot run scripts under the current policy.
- 2026-11-23: A hidden monitor is still checked and still alerts. It is left out of the status page, its banner and the "affects" line of incidents.

- 2026-11-23: Times on the status page are shown with a numeric offset such as `+08:00`, not a zone abbreviation. Abbreviations are ambiguous: "PST" is both Pacific and Philippine time.
- 2026-11-23: The daily uptime bars count UTC days whatever timezone a project sets. Per-zone days would need rollups per zone.
- 2026-11-23: Badge colours are fixed hex values because a badge is shown on other sites, where the page's stylesheet does not apply.
- 2026-11-23: Logos and favicons are uploaded files stored in the database, chosen by the user over allowing image URLs from other sites. The page stays self-contained and the content security policy stays strict.
- 2026-11-23: Custom scripts and tracking codes on status pages are not planned. A status page runs no script at all, and that rule is worth more than analytics.

- 2026-11-23: Ping uses an unprivileged ICMP socket from `golang.org/x/net`, so uppies needs no root and no `ping` program.
- 2026-11-23: A maintenance window covers a whole project, not single monitors. Checks stop entirely during a window, which is simpler than checking and then discarding the result.
- 2026-11-23: The SMTP password comes from the environment, never from a flag, so it does not show in a process list.
- 2026-11-23: Certificate warnings are alerts only. They are not in the timeline, because the timeline's kinds are fixed by a schema constraint and adding one needs a table rebuild.

## Do not repeat

- Do not put the raw check error into text that can become public. A test caught this once (`TestStatusPageShowsOnlyWhatIsPublic`).
- `sqlite3 .lint fkey-indexes` reports `daily_rollups(monitor_id)` and `incident_monitors(incident_id)` as unindexed. Both are the leading column of a WITHOUT ROWID primary key, so the report is a false positive.
- Do not undo a temporary test edit with `git checkout <file>` while that file has other uncommitted changes. It discarded a new migration once. Copy the file aside and copy it back.
- Headless Chrome will not lay a page out narrower than about 500 pixels. For phone widths, use device metrics emulation over the DevTools protocol.

## Next

`ROADMAP.md` holds the planned order. The basics are done. The user wants a
design pass on the status page and admin pages before any new feature. Work one item at a time, one signed commit per step.

## Open risks

- If uppies stops for longer than two check intervals, the gap is not counted as downtime or as covered time. Uptime figures then describe only the time uppies was running.
- If the login rate limit sits behind a reverse proxy, every visitor shares the proxy's address, so five failed sign-ins lock everyone out for five minutes. Reading the client address from a trusted proxy header would fix this.
- The session cookie is marked secure only when the request is TLS or carries `X-Forwarded-Proto: https`.
- Monitors can target any address the server can reach, private networks included. That is intended for an admin-only tool, and it matters if more user roles are added later.

## Running or held

Nothing. The demo server and browser tab used for captures on 2026-11-22 were stopped and closed.
