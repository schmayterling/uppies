# uppies

A small self-hosted uptime monitor with incident management, a timeline and
public status pages for several projects from one install.

Uppies is under active development. See "Not built yet" for planned features.

## What it does today

- Checks HTTP(S) URLs, TCP ports, DNS names and ping targets on a schedule
  you set per monitor.
- Watches scheduled jobs: a push monitor goes down when a job stops reporting in.
- Warns 14, 7, 3 and 1 days before the certificate of an HTTPS target expires.
- Takes planned maintenance windows. Nothing is checked during one, so it
  neither alerts nor counts as downtime, and the status page announces it.
- Waits for retries before it calls something down, and for good checks before
  it calls it up again, so one failed request does not raise an alarm.
- Serves one public status page per project at `/s/<address>`, with a 90-day
  uptime bar per monitor. The pages are plain HTML and work without JavaScript.
- Lets you choose how each monitor handles confirmed outages: do nothing,
  create a private draft, or publish an incident. On recovery, it can leave
  its incidents open, resolve drafts, or resolve drafts and published incidents.
  The defaults create and resolve drafts. Manual incidents stay under your control.
  Published incidents carry dated updates with a status (investigating, identified,
  monitoring, resolved).
- Keeps a timeline per project of every outage, recovery and incident update.
- Sends an alert when a monitor goes down and when it recovers, by email or
  to a plain webhook (JSON), Discord or Slack. The alert says what failed, why, and on
  recovery for how long.
- Groups monitors into categories. Each status page can draw a component as
  daily bars, a response-time line, uptime figures, or a name and state.
  A component can stay visible without contributing to the page's overall status.
- Keeps an incident history page per project, with a year map of incidents and
  a timeline of every update.
- Lets you shape each status page: logo, favicon, tagline, link to your homepage, accent
  colour, light or dark theme, 7 to 365 days of history, timezone, footer
  text, your own CSS, and which monitors are shown. A page can live on its own
  domain, drop the "powered by" line and ask search engines to stay away.
- Serves a status badge per project at `/s/<address>/badge.svg` for readmes
  and other sites.
- Has an admin with more than one account, its own theme, and instance
  settings for the public list's site name, navbar links, and visibility.
- Offers a [versioned JSON API](API.md) for status pages, monitors, incidents,
  maintenance and alert channels, with scoped API tokens.
- Runs as one binary with SQLite by default, with PostgreSQL, MariaDB,
  embedded Turso SQLite, and Turso PostgreSQL available as alternatives.

## Run it

You need Go 1.27 or newer.

```sh
go build -o uppies ./cmd/uppies
./uppies -addr :8080 -db uppies.db
```

Open `http://localhost:8080`. The first visit asks you to create the admin
account. After that, add monitors under "monitors" and alert channels under
"alerts". Both belong to the install. Then add a status page and choose in its
editor which monitors it shows, in which order and drawn how. One monitor can be
on several status pages.

uppies cannot tell you that it has stopped running itself. To cover that, pass
`-heartbeat-url` with the address of an outside service that expects a request
every minute and alerts you when the requests stop.

For email alerts, start uppies with `-smtp-addr host:port` and `-smtp-from
address`. If the server needs a sign-in, put it in the `UPPIES_SMTP_USERNAME`
and `UPPIES_SMTP_PASSWORD` environment variables. uppies uses STARTTLS when
the server offers it. Port 465, which expects TLS from the first byte, is not
supported.

Ping uses an unprivileged socket. On Linux the user running uppies must be
inside the range of the `net.ipv4.ping_group_range` setting.

### Choose a database

Set `-db-backend` or `UPPIES_DB_BACKEND` when starting Uppies.

| Engine | Backend | Connection |
| --- | --- | --- |
| SQLite | `sqlite` | `-db /data/uppies.db`. This is the default. |
| PostgreSQL | `postgres` | `UPPIES_DATABASE_URL` with a PostgreSQL connection string. |
| MariaDB | `mariadb` | `UPPIES_DATABASE_URL` with a Go MySQL driver DSN, such as `user:password@tcp(db:3306)/uppies?tls=true`. |
| Turso SQLite | `turso-sqlite` | `-db /data/uppies-turso.db` for a separate embedded Turso file. |
| Turso PostgreSQL | `tursopg` | `UPPIES_DATABASE_URL` pointing to the private service in Compose. |

Create a dedicated database for each server-backed instance. Uppies installs
its schema on first start. Each backend keeps its own data.

### Run with Docker Compose

From a checkout, pull the Uppies image and start the stack.

```sh
docker compose pull uppies
docker compose up -d --build
```

Open `http://localhost:8080` to create the first admin account. Set
`UPPIES_PORT` or `UPPIES_BIND` to change the default loopback binding.

Compose builds the pinned Turso PostgreSQL preview from `Dockerfile.tursopg`
and stores its database in `tursopg-data`. The service uses a private Docker
network because its wire protocol has no authentication or TLS. Run one
Uppies process against that database. The image enables foreign-key checks
on every connection, which Uppies verifies at startup.

For an existing SQLite install, keep the same Compose project name and
start `compose.sqlite.yaml` to retain the `uppies-data` volume and backups.

```sh
docker compose -f compose.sqlite.yaml up -d
```

The [container workflow](.github/workflows/container.yml) publishes
`latest` and `sha-<commit>` tags from `main`. Set `UPPIES_IMAGE` to
a specific tag or digest to pin a deployment. For a private package,
sign in to `ghcr.io` with a token that has `read:packages` before pulling.

To build and run an image from a checkout, use:

```sh
docker build -t uppies:local .
UPPIES_IMAGE=uppies:local docker compose up -d --build
```

To serve a status page on its own domain, enter the hostname in the project's
settings and point that hostname at uppies through your reverse proxy. The
proxy must pass the original `Host` header on.

Put a reverse proxy with TLS in front of uppies before you expose it to the
internet. The proxy must set the `X-Forwarded-Proto` header so that the
session cookie is marked secure.

Set **public site url** under **instance** to the preferred origin, such as
`https://status.example.com`. Uppies uses it for canonical URLs, link
previews, and `/robots.txt` and `/sitemap.xml`. A status page with a custom
domain uses that domain, with a separate sitemap at its root. Pages set to
noindex are omitted. Turning off the public list omits it and its entries
from the instance sitemap.

## See it with example data

```sh
./uppies -db demo.db -demo
```

`-demo` fills an empty database with example projects: every monitor kind,
monitors that are up, down, paused and not yet checked, 90 days of history,
incidents, maintenance windows, alert channels and three differently styled
status pages. It never adds anything to a database that already has projects.
The example monitors check real public addresses such as `example.com` and
`1.1.1.1`, and addresses reserved for documentation that never answer.

## Develop

```sh
go build ./... && go vet ./... && go test -race ./...
```

`PROGRESS.md` records what is done and how it was verified. `ROADMAP.md` is
the planned order of work.

## Not built yet

Status page subscriptions, alert message templates, the config file mode and
checks from more than one place. [ROADMAP.md](ROADMAP.md) has
the full list and the order.

## License

MIT. See [LICENSE](LICENSE).
