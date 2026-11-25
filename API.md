# Uppies API v1

In the admin sidebar, open **api tokens**. Name the token, choose 30, 90 or
365 days, and select its permissions. Uppies shows the secret once at
creation. Read and write grants are separate and apply across the
installation. An empty selection grants zero permissions. Any admin can
edit a live token's permissions or revoke it. Those changes and expiry
take effect on the next request. Deleting an admin also deletes its tokens.

Send `Authorization: Bearer <token>` with every request, including from a
signed-in browser. Use HTTPS for remote calls. Set `UPPIES_URL` to the
server URL and `UPPIES_API_TOKEN` to the secret. These commands require
`status_pages:read` and `status_pages:create`, respectively.

```sh
curl --fail-with-body -H "Authorization: Bearer $UPPIES_API_TOKEN" \
  "$UPPIES_URL/api/v1/status-pages"

curl --fail-with-body -H "Authorization: Bearer $UPPIES_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"Example Status","slug":"example"}' \
  "$UPPIES_URL/api/v1/status-pages"
```

## Requests and responses

Add `/api/v1` before each path below. Use positive decimal IDs without
signs or leading zeros, and use strings for IDs in JSON. Send RFC 3339
timestamps with an offset or `Z`. Responses use UTC, at millisecond
precision.

Send a single JSON object of up to 64 KiB with
`Content-Type: application/json`. Unknown or duplicate keys, trailing
values, wrong types and nulls produce `400 invalid_json`. PATCH changes
supplied fields and the dependent settings described below. Use `""` or
`[]` where a field allows clearing.

GET returns `200`. HEAD uses the same permission as GET. Create requests
return `201`, a `Location` header with the new API path and an object such as
`{"id":"42"}`.
PATCH, PUT, DELETE, and actions that end maintenance, publish an incident
or add an update return `204`. Retrying a create or append can add another
record.

### Pagination

Lists return `items` in ascending ID order. Use the response's
`next_after` as the `after` query parameter. A final or empty page returns
`"next_after":null`. Use positive decimal integers for `after` and `limit`.
The default limit is 50, capped at 200.

### Errors

Check `error.code` or the HTTP status. The response includes
`error.message` and, for a field error, `error.field`.

```json
{"error":{"code":"invalid_field","field":"title","message":"Use text between 1 and 200 characters."}}
```

| Status | Codes |
| --- | --- |
| 400 | `invalid_json`, `invalid_query` |
| 401 | `invalid_token` for missing, invalid, expired or revoked tokens |
| 403 | `insufficient_permission`, `cross_origin` |
| 404 | `not_found` for a missing path, invalid ID or resource outside the requested page |
| 405 | `method_not_allowed`. The allowed methods are in the `Allow` header. |
| 409 | `slug_taken`, `domain_taken`, `maintenance_not_started` |
| 413 | `body_too_large` |
| 415 | `unsupported_media_type` |
| 422 | `invalid_field`, `unknown_monitor`, `unknown_alert_channel` |
| 500 | `internal_error` |
| 503 | `unavailable` for a timeout |

## Endpoints and permissions

### Status pages and layouts

| Method and path | Permission |
| --- | --- |
| `GET /status-pages` | `status_pages:read` |
| `POST /status-pages` | `status_pages:create` |
| `GET /status-pages/{page}` | `status_pages:read` |
| `PATCH /status-pages/{page}` | `status_pages:update` |
| `DELETE /status-pages/{page}` | `status_pages:delete` |
| `GET /status-pages/{page}/layout` | `status_pages:read` |
| `PUT /status-pages/{page}/layout` | `status_pages:update` |

POST takes `name` and `slug`. Use 1 to 64 lowercase letters, digits or
internal hyphens for `slug`. It fixes the public address at `/s/{slug}`.
Reads return `id`, `slug` and the settings below. PATCH accepts these
settings.

| Setting | Accepted values | Default |
| --- | --- | --- |
| `name` | 1 to 120 characters | Required on create |
| `tagline`, `footer_text`, `custom_css` | Up to 200, 500 and 20,000 characters, respectively. The tagline is plain text for the page list and link previews. The footer is Markdown. | `""` |
| `description` | Markdown up to 2,000 characters, shown above the page's status | `""` |
| `homepage_url` | HTTP(S) URL with a host, up to 2,048 characters, or `""` | `""` |
| `nav_links` | Up to six `label \| URL` lines, 2,000 characters total. Use HTTP(S) URLs, with up to 40 bytes per label. | `""` |
| `navbar_brand` | `icon`, `icon-name`, `name` | `icon-name` |
| `navbar_link` | `status` for this page, `list` for the page list, `homepage` for `homepage_url` | `status` |
| `domain` | Hostname up to 253 characters, or `""` | `""` |
| `timezone` | Recognized timezone name up to 64 characters, such as `Asia/Manila`. It sets where the years of the page's incident history begin. Visitors see times in their browser's timezone, else the instance default. | `UTC` |
| `theme` | `auto`, `light`, `dark` | `auto` |
| `surface` | `neutral`, `paper` | `neutral` |
| `corners` | `sharp`, `soft`, `round` | `soft` |
| `font` | `system`, `rounded`, `serif`, `mono` | `system` |
| `accent_hue` | Integer from 0 to 360 | 200 |
| `history_days` | Integer from 7 to 365 | 90 |
| `backfill` | `none`, `up`, `degraded`, `down` | `none` |
| `graph_fill` | `gradient`, `filled`, `none`, or `""` to inherit from the instance | `""` |
| `announcement` | Pinned Markdown up to 1,000 characters. Use `""` to hide it. | `""` |
| `announcement_color` | `blue`, `green`, `amber`, `red`, `neutral`, `accent`, or `""` for blue | `""` |
| `show_face`, `show_powered_by` | Booleans | `true` |
| `hide_quiet_days`, `noindex` | Booleans | `false` |

Use a nonempty `homepage_url` when `navbar_link` is `homepage`.
To clear the URL, switch the link to `status` or `list` in the same PATCH.

Layout PUT replaces both arrays. Supply `categories` and `monitors` in
display order, using `[]` to clear either. Names in `categories` must be
distinct and 1 to 80 characters after trimming. Use each `monitor_id` at
most once.
`category` must match a name from the array, or be empty or omitted.

```json
{
  "categories": ["Backend"],
  "monitors": [{"monitor_id":"1","category":"Backend","display":"latency"}]
}
```

`display` is `bars` by default, with `latency`, `summary` and `minimal`
also available. An empty or omitted `graph_fill` inherits from the page.
`exclude_from_status` defaults to `false`. Set it to `true` to show the
monitor and omit it from the page's overall status. Layout GET returns
the same fields.

Deleting a page removes its placements, incidents and maintenance. Monitors
and alert channels belong to the installation.

### Monitors

| Method and path | Permission |
| --- | --- |
| `GET /monitors` | `monitors:read` |
| `POST /monitors` | `monitors:create` |
| `GET /monitors/{monitor}` | `monitors:read` |
| `PATCH /monitors/{monitor}` | `monitors:update` |
| `DELETE /monitors/{monitor}` | `monitors:delete` |
| `GET /monitors/{monitor}/alert-channels` | `monitors:read` |
| `PUT /monitors/{monitor}/alert-channels` | `monitors:update` |

Add a monitor to a page with the layout endpoint. Deleting a monitor
removes its history and placements.

| Field | Accepted values | Default |
| --- | --- | --- |
| `name` | 1 to 120 characters | Required on create |
| `tags` | Array of strings split at commas, with whitespace collapsed and case-insensitive duplicates removed. Up to 1,000 characters joined with comma-space. | `[]` |
| `kind` | `http`, `tcp`, `dns`, `ping`, `push` | `http` |
| `target` | Write-only, up to 2,048 characters. Required on create or kind change for checks other than push. | Omit or use `""` for push |
| `keyword` | Write-only HTTP response text to match, up to 200 characters. Use `""` to clear it. | `""` |
| `interval_s` | Integer from 20 to 86,400 seconds | 60 |
| `timeout_ms` | Integer from 1,000 to 60,000 ms in whole seconds | 10,000 |
| `retries` | Integer from 0 to 10. At 2, the third consecutive failure marks the monitor down. | 2 |
| `retry_interval_s` | Integer from 1 to 3,600 seconds between retries | 20 |
| `recovery_checks` | Integer from 1 to 10 consecutive successes | 2 |
| `auto_create_incident` | `off`, `draft`, `publish` | `draft` |
| `auto_resolve_incident` | `off`, `drafts`, `all` | `drafts` |
| `paused` | Boolean | `false` |

For HTTP targets, use an HTTP(S) URL with a host. For TCP, use `host:port`
with a numeric port from 1 to 65,535. For DNS and ping, use a hostname or
address without a scheme or port. Changing to a non-HTTP kind clears the
keyword.

Reads include `id`, the non-secret settings, `status` (`unknown`, `up` or
`down`), `status_since` and nullable `last_check_at`, `last_latency_ms`,
`last_push_at` and `cert_expires_at`.

On a confirmed outage, `draft` and `publish` create an incident on each
page showing the monitor. Sending `publish` needs both `incidents:create`
and `incidents:publish` in addition to the monitor grant. On recovery,
`drafts` resolves drafts from that monitor and `all` also resolves its
published incidents. Sending `all` needs `incidents:update`. Resolve
manual incidents and older incidents without a source by posting an
update.

New monitors start with no alert channels. The `/alert-channels`
subresource reads or replaces assignments with `{"alert_channel_ids":["1"]}`.
Use distinct IDs of existing channels, or an empty array to clear them.

### Alert channels

| Method and path | Permission |
| --- | --- |
| `GET /alert-channels` | `alert_channels:read` |
| `POST /alert-channels` | `alert_channels:create` |
| `GET /alert-channels/{channel}` | `alert_channels:read` |
| `PATCH /alert-channels/{channel}` | `alert_channels:update` |
| `DELETE /alert-channels/{channel}` | `alert_channels:delete` |

Create a channel with `kind` (`webhook`, `discord`, `slack` or `email`)
and a write-only `destination` of up to 2,048 characters. Use a plain
email address or an HTTP(S) webhook URL, as appropriate. PATCH can change
either field. Include the destination when changing the kind. Reads return
`id` and `kind`.

To use a new channel, assign it on the monitor. Editing its destination
redirects alerts from existing assignments. Deleting it clears them.

### Maintenance

| Method and path | Permission |
| --- | --- |
| `GET /status-pages/{page}/maintenance` | `maintenance:read` |
| `POST /status-pages/{page}/maintenance` | `maintenance:create` |
| `GET /status-pages/{page}/maintenance/{window}` | `maintenance:read` |
| `PATCH /status-pages/{page}/maintenance/{window}` | `maintenance:update` |
| `DELETE /status-pages/{page}/maintenance/{window}` | `maintenance:delete` |
| `POST /status-pages/{page}/maintenance/{window}/end` | `maintenance:update` |
| `GET /status-pages/{page}/maintenance/{window}/updates` | `maintenance:read` |
| `POST /status-pages/{page}/maintenance/{window}/updates` | `maintenance:update` |

Create a window with `title` (1 to 200 characters), `starts_at` and either
`ends_at` or `"until_further_notice":true`. A fixed end must be after the
start and in the future when you set up the window or change its dates.
Set `ends_at` to give an open window a fixed end. This clears
`until_further_notice`. Include an end if you set that flag to `false`
on an open window.

Send distinct `monitor_ids` from the page to restrict the window's scope.
Omit the field on create or use `[]` to cover all page monitors. A supplied
list replaces the scope on PATCH. Active windows pause covered monitors'
scheduled checks, including monitors shared across pages.

Reads include past windows and return `id`, `status_page_id`,
`all_monitors` and the settings above. An open window returns
`"ends_at":null`. Use `all_monitors` to tell whole-page coverage from a
scoped window whose selected monitors were removed.

POST to `/end` with no body or `{}` to end an active window. An upcoming
window returns `409 maintenance_not_started`. Ending it again returns
`204` and keeps the recorded end.

GET `/updates` returns messages with `id`, `body` and `at`. POST a
`body` of 1 to 5,000 characters to add a message. You can read or add
messages after the window ends. Deleting a window removes its messages.

Status pages render the bodies of maintenance and incident updates as
Markdown. Two underscores underline text, as in `__text__`, and a line
that starts with `-# ` is small print. Raw HTML shows as typed. The API
stores and returns the text unchanged.

### Incidents

| Method and path | Permission |
| --- | --- |
| `GET /status-pages/{page}/incidents` | `incidents:read` |
| `POST /status-pages/{page}/incidents` | `incidents:create` |
| `GET /status-pages/{page}/incidents/{incident}` | `incidents:read` |
| `PATCH /status-pages/{page}/incidents/{incident}` | `incidents:update` |
| `DELETE /status-pages/{page}/incidents/{incident}` | `incidents:delete` |
| `GET /status-pages/{page}/incidents/{incident}/updates` | `incidents:read` |
| `POST /status-pages/{page}/incidents/{incident}/updates` | `incidents:update` |
| `POST /status-pages/{page}/incidents/{incident}/publish` | `incidents:publish` |

Create a draft with `title` (1 to 200 characters) and `message` (1 to 5,000
characters). The message becomes its first update. Optional fields are
`impact` (`minor`, `major`, `critical`), `status` (`investigating`,
`identified`, `monitoring`, `resolved`) and `monitor_ids`. They default
to `minor`, `investigating` and `[]`. Use `update_title` for an optional
first update heading of up to 200 characters.

PATCH accepts `title`, `impact` and `monitor_ids`. A supplied list of
distinct IDs from the page replaces the affected monitors.

Reads include drafts. Lists return `id`, `status_page_id`, `title`,
`impact`, `status`, `published`, `started_at` and nullable
`resolved_at`. Detail reads also return `monitor_ids`.

GET `/updates` returns `id`, `status`, `body` and `at` for each update.
It includes `title` when set. POST a `status` and a `body` of 1 to 5,000
characters to add an update and set the incident's status. Set an optional
`title` of up to 200 characters for its heading. A blank title uses the
status as the heading.

A `resolved` status sets `resolved_at` on create or append. Appending
another status clears it and reopens the incident.

POST to `/publish` with an empty body or `{}`. Publishing exposes the
existing timeline, including messages written while the incident was a
draft. Repeated calls return `204` and record just one publication event.
Deleting an incident removes its updates.

## Admin console

Use the signed-in admin console for tokens, users, instance settings, image
uploads, global page and monitor ordering, alert tests, push URLs and raw
check errors. Check history, rollups and page events are available in the
HTML views.
