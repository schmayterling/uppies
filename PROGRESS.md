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

Last run: 2026-11-25, build, vet and race tests passed for schema 27.

## Done and verified

Each line names how it was verified.

- Public pages show times in the visitor's browser timezone. `static/tz.js` saves it in a `tz` cookie and reloads once when the page was drawn in another zone. A zone picked in the address still wins and carries into links. Without either, pages use the instance default timezone, a new instance setting that also sets the zone of the admin pages and the maintenance form. Unset means UTC. A page's own timezone left its settings form and only bounds the years of its incident history. Verified by route tests, each seen failing once, and in Chromium and WebKit with emulated New York and Tokyo zones: the first visit loads twice, later visits once, and a picked zone loads once.
- Schema 28 gives each status page a Markdown description, shown above its status and separate from the one-line tagline, which now serves the page list and link previews. The upgrade copies each tagline into the description, so live pages keep their line. "Last check" sits centred under the status. Page and homepage footers render Markdown. Verified by store, settings and API tests, each seen failing once.
- The homepage's recent incidents and the `/incidents` archive show every incident's updates, newest first, with titles, Markdown and read more, as a status page does. They no longer show only started and resolved lines. They also name the affected monitors that the incident's own page shows, through the same `newIncidentView` rule, so a monitor taken off that page is not named. The chips are verified by the same route test, seen failing without the names and when unplaced monitors were named, and in Chromium and WebKit at 1440, 390 and 320 pixels. Verified by the homepage route test, seen failing against the old template and with the update order reversed, and in Chromium and WebKit at 1440, 390 and 320 pixels.
- Schema 27 lets the install have a logo (appearance, "logo"). It takes the status icon's place beside the site name on the public list, the incident archive and the sign-in card (also used by the compact public list), sized like a status page logo: 1.5 times the name's type. The sign-in check test was seen failing without the change, and Chromium and WebKit measured a 36-pixel logo beside the 24-pixel wordmark at 1440, 390 and 320 pixels. Verified by a route test seen failing once, the store workflow on fresh SQLite, Turso SQLite, PostgreSQL, MariaDB and TursoPG, a v26 to v27 upgrade on all five that kept the favicon and sign-in background bytes and served the new logo after a restart, and Chromium and WebKit at 1440, 390 and 320 pixels.
- A logo beside the status page name is 1.5em tall, one line of the name, so it scales with the name's type. A logo shown alone stays 32 pixels. Verified by measuring both modes in Chromium and WebKit at 1440 and 320 pixels (24 and 32 pixels beside 16-pixel text).
- Incident and maintenance updates are written in Markdown (`internal/web/markdown.go`), with `__text__` for underline and `-# ` for small print. An update estimated at more than nine rows shows six lines and a read more toggle built on `<details>`, so status pages still run no script. Verified by renderer and route tests, each seen failing once, and in Chromium and WebKit at 1440, 390 and 320 pixels: the collapsed text always hides more than a line, opens fully, closes again, opens with Enter, and the page never scrolls sideways.
- Public incident lists share the same dated timeline. The seven-day status page history includes active published incidents. Verified by route tests and Chromium and WebKit at desktop and phone widths.
- Schema 26 stores optional incident update titles. Blank titles use the status heading. Verified a populated v25 upgrade, titled updates through the admin and API, and the status-based recovery lifecycle.

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
- Admin frame and overview, design round 1 (2026-11-23): every admin page sits in a 256-pixel sidebar that lists the projects with their state, and the home page is an overview with a strip of counts, a "needs attention" list, one table of all monitors with a 30-day bar, and recent activity. Verified by a handler test. Rendered and measured at 1440 wide in light and dark and at 390 wide: no horizontal overflow, no link or button under 24 pixels tall, four type sizes.
- Demo data (`internal/demo`, flag `-demo`): fills an empty database with every monitor kind and state, history, incidents, maintenance, channels and page settings. Verified by a test and by starting a server on it.
- Design round 2 (2026-11-23), asked for by the user: content centred next to the sidebar, a sidebar that hides and remembers it, icons from Lucide and Simple Icons served as one sprite, dialogs for the new-project form, planning maintenance and the three delete confirmations, and a drawn chevron on every select. Checked in a browser at 1600 wide: equal space left and right of the content, the dialog opens with focus in its first field, the sidebar hides and comes back, no link or button under 24 pixels tall. The admin pages now load one small script (`static/admin.js`). Status pages still load none.
- Design round 3 (2026-11-23), all asked for by the user. Each item is one or two signed commits, covered by handler tests unless noted.
  - Project page, monitor form, incident pages and settings page rebuilt in the new frame. Settings and forms use grouped rows after mux's settings: label and help on the left, control on the right, full content width.
  - Admonitions (error, warning, info, success) for form-level messages.
  - A tooltip per day on every uptime bar, drawn without script.
  - Each monitor chooses how the status page draws it: daily bars, a response time line, three uptime figures, or name and state only (schema version 6).
  - Easy look settings per status page: theme, accent swatches, background (neutral or paper), corners and typeface, with a custom hue and custom CSS under "advanced".
  - Monitor categories, used to group monitors on the status page and named in the admin tables (schema version 7).
  - Past incidents on the status page are listed by day for the last 7 days, quiet days included.
  - An incident history page at `/s/<address>/history`: a year map of incidents in the style of a contribution graph, and a timeline of status updates by day with a link that hides quiet days.
  - Dialogs for adding an alert channel and for posting an incident update.
  - Looked at in a browser on the demo data: settings page, incident page with its dialog, status page with all four displays, history page. Not looked at after their rebuild: monitor form, new-incident form, project page.
- Design round 4 (2026-11-23), asked for by the user.
  - The sidebar lists overview, appearance, instance and users, then a divider and the status pages with their logo or a default icon. Collapsed, it is a 56-pixel rail of icons.
  - Appearance page: theme and accent for the admin and sign-in pages, served as `/theme.css`.
  - Instance page: the instance name and whether the front page lists the status pages (schema version 8, table `instance_settings`).
  - Users page: more than one admin account, removing accounts (never your own, never the last) and changing your own password, which signs out your other devices.
  - Motion: pages cross-fade, controls ease on hover and press, dialogs and messages animate in. All of it uses the token durations, which are zero for people who ask for less motion.
  - Verified by store and handler tests. Checked in a browser: the sidebar is 256 pixels wide and 56 as a rail, labels hide in the rail, the add-user dialog opens and settles at full opacity.
- Design round 5 (2026-11-23).
  - The sidebar no longer shifts while it slides: labels stay on one line and fade, rows stay 28 pixels tall and icons stay at the same position in both states. Measured in a browser.
  - Month labels on the incident map, with a test.
  - The lookback length is any number of days from 7 to 365, and a backfill choice draws the days before a monitor had data as up, degraded, down or no data (schema version 9). The uptime figure only counts measured time. Verified by a handler test.
  - The rebuilt monitor form, new-incident form, project page, appearance and instance pages were measured at 1440 wide: no horizontal overflow and no control under 24 pixels tall.
- Design round 6 (2026-11-23), from the user's feedback on the running app.
  - Fixed: the collapsed sidebar could not be expanded (the hidden wordmark covered the toggle), the "new status page" link opened nothing (links to a dialog now open it), day tooltips were see-through, and admonitions sat flush under headings.
  - Scroll bounce is off on pages, the sidebar, scrolling tables and dialogs.
  - The incident timeline folds each run of quiet days into one muted line, and is paginated at 20 entries.
  - The incident map has weekday labels, month labels and dashed outlines for days that have not happened yet.
  - Visitors can pick the timezone a status page shows its times in, through a script-free form. New status pages start in the server's own timezone.
  - The settings sections are real tabs that show one pane at a time and keep the open tab in the address.
  - Checked in a browser: the toggle works in both sidebar states, the dialog link opens from the overview and from another page, and the tabs switch panes.
- Design round 7 (2026-11-23), from the user's feedback on the running app.
  - Each status page has a top bar with its name, links to its status and incident pages, and up to 6 links of its own set in the settings. The incident history shows the same bar.
  - Planned maintenance is a list of cards. A week without incidents is one sentence and no timeline.
  - The sign-in page has two layouts, split (form in two fifths on the left, background in three fifths on the right) and centered (card over the background), and a plain, gradient or uploaded background of up to 2 MB. Both are set under appearance.
  - Hiding quiet days is a setting of the status page. Visitors no longer have a toggle and the `quiet` address parameter does nothing.
  - The timezone picker is a sentence with a script-free menu of links.
  - The latency line has a tooltip per day. Categories and the history heading have a rule under them.
  - The incident timeline has the day and how long ago it was on the left, and each incident on the right with chips and its updates on a rail: a dot per update in its status colour and a line that stops short of the dots. Open incidents on the status page use the same rail.
  - On admin pages a script replaces each select with a button and a list drawn by the page. Tick boxes, number fields and file fields are restyled in CSS.
  - Checked in a browser: both sign-in layouts (512 and 768 pixels of 1280 in split), an upload of 1.3 MB, the select by click and by key events, the menu opening above its sentence, 90 tooltip columns over the latency line.
- Design round 8 (2026-11-23).
  - The public list of status pages has three layouts, set under appearance: rows and cards are a page with a top bar and a summary sentence, compact is the small centred card.
  - On admin pages a script replaces the date and time fields with a button and a month panel (arrow keys move by day and week, the time is two fields), and the file fields with a button and the file's name. The fields themselves stay in the form and carry the value.
  - Checked with a script that drives real clicks and key presses, in Chromium and in WebKit (Playwright 1.65.1, WebKit build 2388), at 1280 wide in the dark theme and 390 wide in the light theme: sign-in, the select by keyboard and its role tree (a combobox named by its label, a listbox, the chosen option marked selected), the date picker, the file field, the three list layouts, the status page, the history page with its date rule in one column, the timezone menu closing on a tap outside, the latency tooltip. No horizontal overflow anywhere. The first run found two bugs, both fixed: a script error on opening the date picker, and the hidden file field keeping its width.
  - The script and its screenshots are outside the repo, in `captures/check/`.
- Design round 9 (2026-11-23).
  - Each monitor has a detail view: figures for the last 24 hours (checks, failed, p50, p90, p95, p99, slowest) with the uptime over the lookback, a response time chart with one point and one tooltip per half hour, and the uptime bars. Admins get it as the overview tab of the monitor page, with the latest 20 checks and their errors. Visitors get it at `/s/<slug>/monitors/<id>`, linked from the component's name, without targets or errors. A hidden monitor answers 404 there.
  - `/s/<slug>/monitors/<id>/p90.svg`, `p95.svg` and `p99.svg` are 88x31 badges. They are drawn from rectangles only, glyphs included (a 5 by 7 pixel font in `internal/web/badge88.go`), so the same numbers give the same bytes everywhere.
  - Percentiles use the nearest-rank method over the successful raw checks of the last 24 hours. Raw checks are kept for 48 hours, so a longer window would need percentiles in the daily rollups.
  - The demo seeds a check every five minutes for the last day.
  - Checked in Chromium and WebKit at 1280 and 390 wide: the link from the status page, 48 chart slots, the three badges at 88x31, two requests returning the same badge, the admin tabs. No overflow.
- Design round 10 (2026-11-23).
  - The install has a favicon of its own, uploaded under appearance and served at `/favicon.ico`. Status pages keep their own and fall back to it.
  - The public list shows a logo only when a status page has one.
  - A monitor's overview opens with its state as an admonition, with the last error when it is down.
  - Graphs have a fill option: gradient, filled or none. It is set under appearance for the instance, in a status page's look settings and on a monitor, and the narrowest one that is set wins. A fresh install uses gradient.
  - The sidebar has no divider when it is open. Collapsed, a line stands where the section's name would be.
  - Checked in Chromium and WebKit: the gradient and the flat fill on the monitor chart, set through the appearance form, and the sidebar line in both states. The favicon, the list without logos and the admonition are covered by tests and were not rendered.
- Round 11 (2026-11-23): monitors became global, and status pages got an editor. Schema version 15.
  - Monitors belong to the install, not to one status page. `page_monitors` lists what each page shows, with a position, a display and a graph fill per page. One monitor can be on several pages. A monitor that was hidden is on no page. Deleting a status page keeps its monitors.
  - Alert channels belong to the install (`/admin/alerts`). `monitor_channels` says which channels a monitor alerts. A new channel is alerted by every existing monitor. The migration kept each monitor alerting the channels of its old page.
  - Categories are a list of their own, made under `/admin/monitors`, and a monitor's category is the same everywhere. A status page groups its monitors by category, uncategorised first, each category at the place of its first monitor.
  - Maintenance stays per status page and rests every monitor that page shows. An outage drafts an incident on every page that shows the monitor. A monitor's events carry no project: a page's timeline finds them through its monitors.
  - `/admin/monitors` lists every monitor by category. The monitor form chooses category and alert channels. Where and how it is shown is the editor's job.
  - A status page's admin page has tabs: editor, incidents (with planned maintenance), timeline, delete. The editor lists the page's monitors with arrows, dragging, a style and a fill per monitor, taking one off, adding an existing one, the look (theme, accent, background, corners, typeface, graph fill, status icon, quiet days, custom css) and a live preview in a frame. For that frame the policy changed from `frame-ancestors 'none'` to `'self'`.
  - Status pages can be moved up and down, which orders the sidebar and the public list.
  - Settings save themselves: forms marked `data-autosave` are sent when a field changes, which for text is when the focus leaves it. A refused form is sent again the ordinary way so that the page with the problems shows. Without script the save buttons are still there. Forms that create or delete something keep their button.
  - The face is gone. A round icon says the state: check, warning, cross, dash, wrench, or a slash when everything is paused.
  - The migration was tried on a copy of the dev database before the real one was opened with it. Counts matched.
  - Checked in Chromium and WebKit with real clicks: reorder by arrow and by drag, style through the custom select, accent and custom css reaching the preview, take off and add back, autosave on blur in settings, the monitors and alerts pages, no overflow at 1440 and 390 wide.
- Round 12 (2026-11-23). Schema version 16.
  - The list of all monitors is arranged by dragging: a monitor to another place or into another category, a category as a whole by its name. Monitors have a `position` inside their category. `POST /admin/monitors/order` takes the arrangement as ordered `item` fields, `c:<category>` then `m:<id>` for each monitor in it.
  - The monitors table uses the same markup as the other tables. Round status icons are as tall as the text beside them.
  - Checked in Chromium and WebKit: a drag inside a category, into another category and of a whole category, each saved and still there after a reload.
- Round 13 (2026-11-23). Schema version 17.
  - Categories belong to each status page (`page_categories`, and `category` on `page_monitors`). There are no global categories, and a monitor has none of its own. The list of all monitors is flat and ordered by dragging.
  - The editor is its own page, `/admin/projects/<id>/editor`: the left third holds the page's heading and every control, the right two thirds are the preview from the top of the window to the bottom. It arranges monitors and categories by dragging or with arrows, adds and removes categories, and sends the whole arrangement as ordered `item` fields (`c:<category>`, `m:<id>`) with at most one change.
  - 88x31 badges are general: `/s/<slug>/88x31.svg` for a status page and `/s/<slug>/monitors/<id>/88x31.svg` for a component, with `show=status|uptime` (and `p50|p90|p95|p99` for a component), `days=`, `theme=light` and `label=`. The pixel font covers the lowercase alphabet. `p90.svg`, `p95.svg` and `p99.svg` still work.
  - mstack ui pass. Read in full: `runtime/ui.md`, `runtime/ui-checks.md`, `design/README.md`, `design/BRIEF.md`, `design/corrections.md`, `design/polish/rules.md`, `design/polish/checklist.md`, `design/critique/rules.md`. Not read: the rest of `design/` (about 3.7 MB: taste, slop, variants, personality, references, micro, motion and more). `design/SKILL.md` does not exist.
  - Ran `design/critique/_tools/check.mjs` on five public and eight admin pages (admin through a local proxy that adds the session cookie), and `lint-literals.sh` on the styles (0 literals to fix). Fixed from its findings: hover feedback sits inside `(hover: hover)`, tooltips stay outside on purpose. Section headings on public pages step up to a named 20px. Group labels and field labels differ from the text under them. Two font weights per screen instead of three. Five type sizes on the status page instead of six. Running text is capped at 75 characters. Touch screens get 16px form text. Every page has a description and a favicon. The top bar's brand and links share a size. The interface says "status page" where it said "project".
  - Still failing in that checker: R12 wants an `og:image` on every page, and the logo preview in settings has no fixed size. Not rerun after the last batch: R2 on the editor, R4 on the editor and settings.
- Checks run on 2026-11-23 on the admin overview:
  - Contrast, computed from the rendered colours. Dark theme: every text pairing measured is 7.4:1 or higher. Light theme: the lowest is 5.68:1 (muted headings on the sidebar). The requirement for body text is 4.5:1.
  - Script switched off: an admin page shows each dialog's form in place and hides the buttons that would open it. This needed a fix, because the dialog animation left dialogs invisible without script.
  - Keyboard: the focus order read from the page is skip link, wordmark, sidebar toggle, then the sidebar entries in order. A walk with real key presses was not possible in the test browser, whose tab is in the background and ignores them.
- Status page rendered and measured at 1280 and 390 CSS pixels wide in the light theme: no horizontal overflow, 90 day cells on wide screens and 30 on narrow ones. The dark theme was looked at once before the last style fixes and not measured.

## Done but not verified

- Round 11 in a browser covered the editor, autosave on one settings field, and that the monitors and alerts pages open. Not driven in a browser: making a category, the alert channel boxes on the monitor form, moving status pages, autosave on the appearance, instance and monitor forms.
- The editor's drag reorder has no touch support. The arrows work everywhere.
- The monitors page can only be rearranged by dragging with a mouse: there are no arrows there and no touch support. A monitor's category can still be set in its settings.
- Firefox was not checked. The Firefox build that Playwright 1.65.1 downloads does not start on this machine ("Could not find profile folder"), with a temporary profile and with a fixed one.
- No screen reader was run. The role tree of the custom select was read in Chromium and WebKit, which is what a screen reader is given, but nobody listened to it.
- Safari itself was not driven, only Playwright's WebKit build.
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
- 2026-11-23: Custom scripts and tracking codes on status pages are not planned. A status page runs no script at all, and that rule is worth more than analytics. (Narrowed 2026-11-26: public pages load the one same-origin `tz.js` to read the browser's timezone, at the user's request.)

- 2026-11-23: Ping uses an unprivileged ICMP socket from `golang.org/x/net`, so uppies needs no root and no `ping` program.
- 2026-11-23: A maintenance window covers a whole project, not single monitors. Checks stop entirely during a window, which is simpler than checking and then discarding the result.
- 2026-11-23: The SMTP password comes from the environment, never from a flag, so it does not show in a process list.
- 2026-11-23: Certificate warnings are alerts only. They are not in the timeline, because the timeline's kinds are fixed by a schema constraint and adding one needs a table rebuild.

- 2026-11-23: The admin uses a sidebar plus content layout at desktop density (13-pixel text, 28-pixel controls). References looked at: the dashboards of Uptime Kuma, Peekaping and Checkmate. Taken from them: a sidebar for navigation, a strip of counts, a list of what is down, and a small uptime bar on every monitor row. Not taken: coloured pills on every row and a grid of cards.
- 2026-11-23: An outage and its draft incident are one line in "needs attention", linked to the draft.

- 2026-11-23: The admin pages may use a script. Dialogs and the sidebar toggle need one. Without script, dialogs are shown in place through `static/noscript.css`. Status pages stay free of script.
- 2026-11-23: The sidebar hides completely and does not shrink to a rail of icons, because it has few destinations.
- 2026-11-23: Icons are copied into the repository as one SVG sprite, with their licenses in `THIRD_PARTY.md`. Slack has no icon in Simple Icons, so its channel uses a generic one.

- 2026-11-23: Settings pages follow mux's settings: bordered groups of rows, one save button per form. They fill the content width, as the user asked.
- 2026-11-23: A monitor's category is a plain name on the monitor, not a table of its own. Monitors with the same name group together. Renaming a category means editing its monitors.
- 2026-11-23: The incident history shows one calendar year at a time, in the project's timezone.
- 2026-11-23: Look settings that a form does not send keep their value, so older forms and scripts still save.

- 2026-11-23: There are no roles. Every account can manage everything. Roles wait for a real need.
- 2026-11-23: Maintenance windows stay on each status page's own page, because a window belongs to one status page.
- 2026-11-23: The sidebar's width is the one animated size in the app. Everything else animates only opacity, colour and scale.
- 2026-11-23: A measurement taken in a background browser tab can show a transition's start value, because the tab draws no frames. Finish the animations before reading sizes.

- 2026-11-23: The old `history_days` column stays in the database unused. The new `lookback_days` column replaces it, because changing a CHECK constraint means rebuilding the projects table.
- 2026-11-23: Backfilled days say "no data" in their tooltip and never enter the uptime figure. Painting history green must not change the numbers.

- 2026-11-23 (superseded 2026-11-26 by the browser timezone below): A status page cannot read a visitor's clock without script, so the timezone is a choice the visitor makes, carried in the address as `tz`.
- 2026-11-25: Update text is Markdown rendered by `github.com/yuin/goldmark` v1.8.7, chosen by the user over a parser written in the repository. It follows CommonMark and has no dependencies of its own. Raw HTML and setext headings are switched off, `__text__` underlines instead of making text strong, headings become h5 and h6 under the incident's h4, and images become links because the content security policy blocks images from other sites.
- 2026-11-25: The install's logo is a new kind in `instance_images`, so schema 27 widens that table's CHECK. SQLite and Turso SQLite rebuild the table, PostgreSQL drops and adds the constraint, MariaDB redefines the column, and TursoPG rebuilds the table because it cannot drop a constraint. Rejected: a separate table for one image.
- 2026-11-26: Public pages default to the visitor's browser timezone, read by a small same-origin script, with the instance default timezone as the one fallback. The user chose this over keeping each page's timezone as its fallback. An unset instance timezone means UTC, which the admin pages showed before, so tests and installs do not depend on the server's zone.
- 2026-11-26: The status page description is its own field. The user chose to start it from each page's tagline over starting empty or keeping the tagline under the status.
- 2026-11-25: Whether an update collapses is decided on the server from its length, because a page without script cannot measure overflow. The threshold sits above the collapsed height so the toggle never opens onto nothing.

## Do not repeat

- Do not put the raw check error into text that can become public. A test caught this once (`TestStatusPageShowsOnlyWhatIsPublic`).
- `sqlite3 .lint fkey-indexes` reports `daily_rollups(monitor_id)` and `incident_monitors(incident_id)` as unindexed. Both are the leading column of a WITHOUT ROWID primary key, so the report is a false positive.
- Do not undo a temporary test edit with `git checkout <file>` while that file has other uncommitted changes. It discarded a new migration once. Copy the file aside and copy it back.
- Chain the commit after the tests with `&&`. A commit went in once with a failing test because the chain used `;`.
- The `git checkout <file>` mistake above happened a second time on 2026-11-23, after a test mutation in a file that also held new code. Always copy the file aside first.
- A patch script that stops halfway must stop the commit too. Test its exit code before the commit chain. One commit (`1482684`) carries a message that claims more than it holds because of this.
- After changing how a control is laid out, click it in every state. The sidebar toggle was measured but never clicked while collapsed, and it was broken.
- When a form becomes a dialog, find every link that pointed at the old form. One link kept pointing at an anchor that no longer opened anything.
- In zsh, `rm -f name*` fails when nothing matches and stops a command chain. Name the files.
- Do not drop goldmark's indented code block parser: without it an indented line ends parsing and every word after it disappears. `TestUpdateMarkdownKeepsEveryWord` guards this.
- Playwright's WebKit screenshots inject a style that the content security policy refuses, which logs a console error on every page. Read the console in a pass without screenshots.
- Headless Chrome will not lay a page out narrower than about 500 pixels. For phone widths, use device metrics emulation over the DevTools protocol.

## Next

`ROADMAP.md` holds the planned order. The basics are done. The user wants a
design pass on the status page and admin pages before any new feature. Thirteen rounds are done (see above). Still unchecked: a keyboard walk with real key presses, and the motion in Safari and Firefox. Work one item at a time, one signed commit per step.

## Open risks

- If uppies stops for longer than two check intervals, the gap is not counted as downtime or as covered time. Uptime figures then describe only the time uppies was running.
- If the login rate limit sits behind a reverse proxy, every visitor shares the proxy's address, so five failed sign-ins lock everyone out for five minutes. Reading the client address from a trusted proxy header would fix this.
- The session cookie is marked secure only when the request is TLS or carries `X-Forwarded-Proto: https`.
- Monitors can target any address the server can reach, private networks included. That is intended for an admin-only tool, and it matters if more user roles are added later.

## Running or held

Nothing. The demo server and browser tab used for captures on 2026-11-22 were stopped and closed.
