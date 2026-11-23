# Uppies API v1

Create an API token from **api tokens** in the signed-in admin sidebar. Give it a
name, choose a lifetime of 30, 90, or 365 days, and select the actions it needs.
The creation page displays the secret once. Store it where your integration
keeps credentials. The token list shows metadata and lets any admin edit a live
token's permissions or revoke it. Removing an admin account also removes the
tokens it created.

Send the token in `Authorization: Bearer <token>` on every request. Use HTTPS
for remote access. The API authenticates the bearer header independently of
browser sessions. Use the header for credentials and keep URLs free of tokens.
Each endpoint requires its listed permission. Read and write are separate
grants. An empty selection yields a token with zero permissions. Permission
changes, expiry and revocation apply on the next request.

```sh
curl --fail-with-body \
  -H "Authorization: Bearer $UPPIES_API_TOKEN" \
  "$UPPIES_URL/api/v1/status-pages"
```

```sh
curl --fail-with-body \
  -H "Authorization: Bearer $UPPIES_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"Acme Status","slug":"acme"}' \
  "$UPPIES_URL/api/v1/status-pages"
```

## Requests and responses

All paths below start with `/api/v1`. IDs are positive decimal strings.
Timestamps use RFC 3339 with a timezone on input and UTC on output. Uppies
stores timestamps at millisecond precision. Send request objects as
`Content-Type: application/json` with up to 64 KiB. Bodies with unknown keys,
duplicate keys, explicit nulls, trailing values, or mismatched types receive
`400`. Omitted fields in a PATCH keep their values. Use an empty string or array
where a field's contract allows clearing it. HEAD uses the same permission as GET.

Collection responses have `items` and `next_after`. They use ascending ID
order. The optional `limit` defaults to 50 and is capped at 200. Pass the
previous response's `next_after` as the `after` query parameter to continue.
A final or empty page returns `"next_after":null`.

```json
{"items":[{"id":"1","kind":"webhook"}],"next_after":null}
```

Resource creation returns `201`, a `Location` header and a JSON object with
the created resource's `id`. Updates, deletes, publishing and appending an
incident update return `204`. Creating a status page with an existing slug
returns `409`. A retry of other create or append requests may create another
record. Publishing an already public incident returns `204` with no added
timeline event.

API errors use `error.code` and `error.message`, with `error.field` for
field-specific validation. Branch on the status or code.

```json
{"error":{"code":"invalid_field","field":"title","message":"Use text between 1 and 200 characters."}}
```

| Status | Codes |
| --- | --- |
| 400 | `invalid_json`, `invalid_query` |
| 401 | `invalid_token` for a missing, invalid, expired or revoked token |
| 403 | `insufficient_permission`, `cross_origin` |
| 404 | `not_found` for a missing resource or a child of a different page |
| 405 | `method_not_allowed`, with an `Allow` header |
| 409 | `slug_taken`, `domain_taken` |
| 413 | `body_too_large` |
| 415 | `unsupported_media_type` |
| 422 | `invalid_field`, `unknown_monitor`, `unknown_alert_channel` |
| 500 | `internal_error` |
| 503 | `unavailable` when the request times out |

## Endpoints and permissions

| Method and path | Permission |
| --- | --- |
| `GET /status-pages` | `status_pages:read` |
| `POST /status-pages` | `status_pages:create` |
| `GET /status-pages/{page}` | `status_pages:read` |
| `PATCH /status-pages/{page}` | `status_pages:update` |
| `DELETE /status-pages/{page}` | `status_pages:delete` |
| `GET /status-pages/{page}/layout` | `status_pages:read` |
| `PUT /status-pages/{page}/layout` | `status_pages:update` |
| `GET /monitors` | `monitors:read` |
| `POST /monitors` | `monitors:create` |
| `GET /monitors/{monitor}` | `monitors:read` |
| `PATCH /monitors/{monitor}` | `monitors:update` |
| `DELETE /monitors/{monitor}` | `monitors:delete` |
| `GET /monitors/{monitor}/alert-channels` | `monitors:read` |
| `PUT /monitors/{monitor}/alert-channels` | `monitors:update` |
| `GET /alert-channels` | `alert_channels:read` |
| `POST /alert-channels` | `alert_channels:create` |
| `GET /alert-channels/{channel}` | `alert_channels:read` |
| `PATCH /alert-channels/{channel}` | `alert_channels:update` |
| `DELETE /alert-channels/{channel}` | `alert_channels:delete` |
| `GET /status-pages/{page}/maintenance` | `maintenance:read` |
| `POST /status-pages/{page}/maintenance` | `maintenance:create` |
| `GET /status-pages/{page}/maintenance/{window}` | `maintenance:read` |
| `PATCH /status-pages/{page}/maintenance/{window}` | `maintenance:update` |
| `DELETE /status-pages/{page}/maintenance/{window}` | `maintenance:delete` |
| `POST /status-pages/{page}/maintenance/{window}/end` | `maintenance:update` |
| `GET /status-pages/{page}/maintenance/{window}/updates` | `maintenance:read` |
| `POST /status-pages/{page}/maintenance/{window}/updates` | `maintenance:update` |
| `GET /status-pages/{page}/incidents` | `incidents:read` |
| `POST /status-pages/{page}/incidents` | `incidents:create` |
| `GET /status-pages/{page}/incidents/{incident}` | `incidents:read` |
| `PATCH /status-pages/{page}/incidents/{incident}` | `incidents:update` |
| `DELETE /status-pages/{page}/incidents/{incident}` | `incidents:delete` |
| `GET /status-pages/{page}/incidents/{incident}/updates` | `incidents:read` |
| `POST /status-pages/{page}/incidents/{incident}/updates` | `incidents:update` |
| `POST /status-pages/{page}/incidents/{incident}/publish` | `incidents:publish` |

## Status pages and layouts

Create a page with `name` (1–120 characters) and `slug` (1–64 lowercase
letters, digits or internal hyphens). The slug determines the public address
at `/s/{slug}` and is fixed after creation. A new API-created page starts in
UTC. Read responses contain `id`, `slug` and the settings below. PATCH accepts
any subset of these settings.

| Fields | Values |
| --- | --- |
| `name`, `tagline`, `footer_text`, `custom_css` | Name 1–120 characters. The others accept 0–200, 0–500 and 0–20,000 respectively. |
| `homepage_url`, `nav_links` | An HTTP(S) URL up to 2,048 characters, and up to six lines of `label \| URL` totaling up to 2,000 characters. Empty strings clear them. |
| `domain` | A hostname up to 253 characters, or an empty string. |
| `timezone` | A recognized timezone name such as `Asia/Manila`. |
| `theme`, `surface`, `corners`, `font` | `auto`, `light` or `dark`. `neutral` or `paper`. `sharp`, `soft` or `round`. `system`, `rounded`, `serif` or `mono`. |
| `navbar_brand` | `icon`, `icon-name` or `name`. |
| `navbar_link` | `status` (default), `list` or `homepage`. `homepage` requires a `homepage_url`. |
| `announcement`, `announcement_color` | Pinned plain text up to 1,000 characters. An empty message hides it. Choose `blue`, `green`, `amber`, `red`, `neutral`, `accent`, or an empty string for blue. |
| `accent_hue`, `history_days` | Integers 0–360 and 7–365 respectively. |
| `backfill`, `graph_fill` | `none`, `up`, `degraded` or `down`. `gradient`, `filled`, `none` or an empty string to inherit. |
| `show_face`, `hide_quiet_days`, `show_powered_by`, `noindex` | Booleans. |

Layout GET and PUT use the shape below. PUT replaces the categories and
monitor placements together. Supply both arrays and use empty arrays to
clear them. Categories are unique, 1–80 characters, and kept in array
order. The `monitors` array orders the placements. Each monitor ID may
appear at most once per page. Omit `category` or use an empty string for an
uncategorized monitor. A nonempty category must appear in `categories`.
`display` is `bars` (default), `latency`, `summary` or `minimal`.
`graph_fill` uses the page's options and inherits the page if empty or omitted.
Set `exclude_from_status` to `true` to keep a monitor visible while removing
its contribution to the overall state of this page. It defaults to `false`.

```json
{"categories":["Backend"],"monitors":[{"monitor_id":"1","category":"Backend","display":"latency","graph_fill":""}]}
```

Deleting a page removes its placements, incidents, maintenance windows and
other page-specific records. Its monitors and alert channels remain available
to the install.

## Monitors

Monitors belong to the install. Use the page layout endpoint to display one
on a status page. A new monitor starts with no channel assignments.

| Writable field | Values |
| --- | --- |
| `name` | Required on create. 1–120 characters. |
| `tags` | Array of strings. Trims and collapses whitespace and deduplicates case-insensitively. Up to 1,000 characters joined with comma-space. Omit to preserve tags or send `[]` to clear them. |
| `kind` | `http` (default), `tcp`, `dns`, `ping` or `push`. |
| `target` | Write-only. Required on create and kind changes for `http`, `tcp`, `dns` and `ping`. Use an HTTP(S) URL, a host with a numeric port (1–65535), or a hostname/address respectively. Maximum 2,048 characters. Push uses an omitted or empty target. |
| `keyword` | Write-only. HTTP response text to match, up to 200 characters. Empty clears it. |
| `interval_s` | Integer 20–86,400. Default 60. |
| `timeout_ms` | Integer 1,000–60,000 in whole-second increments. Default 10,000. |
| `retries` | Additional consecutive failures after the initial failed check. Integer 0–10. Default 2, so the third failed check marks the monitor down. |
| `retry_interval_s` | Integer 1–3,600. Default 20. |
| `recovery_checks` | Consecutive successful checks needed to confirm recovery. Integer 1–10. Default 2. |
| `auto_create_incident` | `off`, `draft` (default), or `publish`. On a confirmed outage, create a draft or published incident on each page showing the monitor. |
| `auto_resolve_incident` | `off`, `drafts` (default), or `all`. On confirmed recovery, resolve drafts only or both drafts and published incidents created by this monitor. |
| `paused` | Boolean. Default false. |

Reads return the ID and non-secret settings, plus `status` (`unknown`,
`up` or `down`), `status_since`, and nullable `last_check_at`,
`last_latency_ms`, `last_push_at` and `cert_expires_at`. The API restricts
raw check errors and the push reporting URL to the admin console.
Setting `publish` also requires `incidents:create` and `incidents:publish`.
Setting `all` also requires `incidents:update`. Omit either policy in PATCH
to keep its current value. Manual incidents and incidents created before source
tracking stay open until someone resolves them.

The monitor's `/alert-channels` subresource reads or replaces assignments
with `{"alert_channel_ids":["1"]}`. Each ID must be distinct and refer to
an existing channel. An empty array clears the assignments. Deleting a
monitor removes its history and its placements while keeping its pages.

## Alert channels

POST requires `kind` and `destination`. PATCH can update either and requires
a destination if the kind changes. Kind is `webhook`, `discord`, `slack`
or `email`. The write-only destination is an HTTP(S) webhook URL or an email
address, up to 2,048 characters. Reads return `id` and `kind`.

Channels created through the API start unassigned. Add them to monitors
through the monitor's assignment endpoint. Updating a channel changes the
destination for existing assignments. Deleting one removes its assignments.
Channel configuration permissions grant control over where an install can
send alerts.

## Maintenance

Maintenance windows belong to a status page. The collection includes past
windows. POST requires a title (1–200 characters), `starts_at`, and either a
future `ends_at` after the start or `until_further_notice: true`. Open-ended
windows return `ends_at: null`. Send a scheduled `ends_at` to give one a fixed
end. PATCH preserves omitted fields.

Set `monitor_ids` to a list of distinct IDs on the page to scope the window.
Omit it on create or send an empty array to cover all page monitors. Reads
include `all_monitors`, `monitor_ids`, and `until_further_notice` alongside
the original window fields. Removing a selected monitor does not broaden
the scope.

POST to `/end` with no body or `{}` to end a running window. An already ended
window succeeds without changing its end. An upcoming window returns `409`.
GET `/updates` lists dated messages (`id`, `body`, `at`), including after
the window ends. POST a `body` of 1–5,000 characters to add an update, returning
`204`. Deleting a window removes its updates.

## Incidents

Incident reads include drafts and published incidents. The collection
returns `id`, `status_page_id`, `title`, `impact`, `status`,
`published`, `started_at` and nullable `resolved_at`. A single incident
also returns `monitor_ids`. Use `/updates` to read its timeline. Each
update has `id`, `status`, `body` and `at`. Timeline content can
include private notes or automatic draft messages.

POST an incident with `title` (1–200 characters) and `message` (1–5,000
characters) for its first update. Optional fields are `impact` (`minor`,
`major`, `critical`), `status` (`investigating`, `identified`,
`monitoring`, `resolved`) and `monitor_ids`. Defaults are `minor`,
`investigating` and an empty list. The incident starts as a draft.

PATCH accepts `title`, `impact` and `monitor_ids`. When supplied, monitor
IDs replace the affected monitors. Every referenced monitor must appear on the
incident's page. POST to `/updates` with a `status` and `body` (1–5,000
characters) to append a timeline message and set its current status. POST to
`/publish` to make the incident public. Omit the body or send an empty object.

## Admin console

Token management, users, instance settings, status-page image uploads, page
and monitor list ordering, alert test delivery, push URL retrieval and
raw check diagnostics are available through the signed-in admin console.
Monitor check history, rollups and page event history use the existing HTML
views.
