# Progress

This file is the state of the project for whoever picks it up next.
Dates are ISO dates in +08:00 unless a time says otherwise.

## Goal

uppies is a self-hosted uptime monitor: one Go binary, one SQLite file,
server-rendered pages. Version 1 covers monitors, incident management, a
timeline view and public status pages for several projects from one install.
The product draft and the research behind it live outside this repository, in
`../research/uppies-research.md`.

## Check that everything still works

```sh
go build ./... && go vet ./... && go test -race ./...
```

Last run: 2026-11-22, all five packages passed.

## Done and verified

Each line names how it was verified.

- State machine for monitors (`internal/health`): retries before down, good checks before up, downtime accounting. Verified by table tests, each seen failing once.
- SQLite store (`internal/store`): schema version 1, projects, monitors, checks, daily rollups, events, incidents, users, sessions. Verified by tests on a temporary database file.
- HTTP and TCP checks (`internal/check`). Verified by tests against local servers.
- Check engine (`internal/engine`): one goroutine per monitor, at most 32 checks at once, reload on change, raw checks pruned after 48 hours. Verified by tests for one check cycle. The scheduling loop itself and pruning are verified only by the manual run below.
- Web layer (`internal/web`): status pages, first-run setup, sign-in with a rate limit, projects, monitors, incidents, timeline. Verified by handler tests for the access rules and by the manual run below.
- Manual end-to-end run on 2026-11-22 with the real binary and a local target: setup, refused requests (303 without a session, 403 cross-site, 422 invalid input, 429 after five failed sign-ins), an outage confirmed 22 seconds after the target stopped (interval 20 s, 2 retries 1 s apart), recovery confirmed 40 seconds after it came back, the draft incident opened and resolved, and a clean exit on SIGTERM.
- Status page rendered and measured at 1280 and 390 CSS pixels wide in the light theme: no horizontal overflow, 90 day cells on wide screens and 30 on narrow ones. The dark theme was looked at once before the last style fixes and not measured.

## Done but not verified

- Colour contrast of the status colours on each surface was not measured. The values come unchanged from the token file.
- Keyboard-only use and screen reader output were not checked.
- Behaviour with many monitors (hundreds) was not measured. The memory and speed targets in the product draft are still goals.
- The admin pages were captured once at 1280 wide before the last style fixes and not again.

## Decisions

- 2026-11-22: Go, embedded SQLite through `modernc.org/sqlite` (no cgo), server-rendered pages. Chosen by the user after the research draft. Rejected: TypeScript on Bun or Node.js, Rust, a single-page app.
- 2026-11-22: SQLite stays the default database. PostgreSQL is the only planned second backend. Turso and KineDB were considered and set aside: Turso's embedded engine is not at 1.0, Turso Cloud would add a network service, and KineDB is not publicly available.
- 2026-11-22: One goroutine per monitor in place of a shared time-ordered queue. It is simpler, and the expected size is 5 to 200 monitors.
- 2026-11-22: Only daily rollups are stored. Hourly rollups wait until a view needs them, and adding them is an additive migration.
- 2026-11-22: Rollups keep the latency sum and maximum, not the p95 that the draft mentions, because a p95 cannot be added up from partial results.
- 2026-11-22: A draft incident never contains the raw check error. That text can name private hosts and becomes public when the draft is published.
- 2026-11-22: The public status page shows a monitor's name and state only, never its target or error text.
- 2026-11-22: A day with under one hour of downtime is drawn as a disruption, a day with one hour or more as an outage. This threshold is a judgment call and has no source.
- 2026-11-22: Cross-site form posts are refused with `http.CrossOriginProtection` from the standard library in place of CSRF tokens.

## Do not repeat

- Do not put the raw check error into text that can become public. A test caught this once (`TestStatusPageShowsOnlyWhatIsPublic`).
- `sqlite3 .lint fkey-indexes` reports `daily_rollups(monitor_id)` and `incident_monitors(incident_id)` as unindexed. Both are the leading column of a WITHOUT ROWID primary key, so the report is a false positive.
- Headless Chrome will not lay a page out narrower than about 500 pixels. For phone widths, use device metrics emulation over the DevTools protocol.

## Next

In this order, one signed commit per step:

1. Alerts: SMTP email and webhook first, sent on confirmed down and up.
2. Scheduled maintenance windows that mute alerts and do not count as downtime.
3. Config file mode: monitors, projects and alert channels from YAML, read-only in the UI.
4. REST API with keys.
5. More monitor kinds: ping, DNS, push for cron jobs, and certificate expiry warnings.
6. Timeline ranges on the status page (24 hours, 7 days, 30 days), which needs hourly rollups.
7. A Dockerfile and a release build.

## Open risks

- If uppies stops for longer than two check intervals, the gap is not counted as downtime or as covered time. Uptime figures then describe only the time uppies was running.
- If the login rate limit sits behind a reverse proxy, every visitor shares the proxy's address, so five failed sign-ins lock everyone out for five minutes. Reading the client address from a trusted proxy header would fix this.
- The session cookie is marked secure only when the request is TLS or carries `X-Forwarded-Proto: https`.
- Monitors can target any address the server can reach, private networks included. That is intended for an admin-only tool, and it matters if more user roles are added later.

## Running or held

Nothing. The demo server and browser tab used for captures on 2026-11-22 were stopped and closed.
