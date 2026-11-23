# uppies

A small self-hosted uptime monitor with incident management, a timeline and
public status pages for several projects from one install.

Status: early. The core works and is tested, but it has not run in production
and several planned features are missing (see "Not built yet").

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
- Opens a private draft incident when a monitor goes down. You decide whether
  to publish it. Published incidents carry dated updates with a status
  (investigating, identified, monitoring, resolved).
- Keeps a timeline per project of every outage, recovery and incident update.
- Sends an alert when a monitor goes down and when it recovers, by email or
  to a plain webhook (JSON), Discord or Slack. The alert says what failed, why, and on
  recovery for how long.
- Groups monitors into categories, and lets each monitor choose how the status
  page draws it: daily bars, a response time line, uptime figures, or just its
  name and state.
- Keeps an incident history page per project, with a year map of incidents and
  a timeline of every update.
- Lets you shape each status page: logo, favicon, tagline, link to your homepage, accent
  colour, light or dark theme, 30, 60 or 90 days of history, timezone, footer
  text, your own CSS, and which monitors are shown. A page can live on its own
  domain, drop the "powered by" line and ask search engines to stay away.
- Serves a status badge per project at `/s/<address>/badge.svg` for readmes
  and other sites.
- Runs as one binary and stores everything in one SQLite file.

## Run it

You need Go 1.27 or newer.

```sh
go build -o uppies ./cmd/uppies
./uppies -addr :8080 -db uppies.db
```

Open `http://localhost:8080`. The first visit asks you to create the admin
account. After that, add a project, then add monitors to it. Alert channels and
the look of the status page are under the project's settings.

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

To run it in a container:

```sh
docker build -t uppies .
docker run -p 8080:8080 -v uppies-data:/data uppies
```

To serve a status page on its own domain, enter the hostname in the project's
settings and point that hostname at uppies through your reverse proxy. The
proxy must pass the original `Host` header on.

Put a reverse proxy with TLS in front of uppies before you expose it to the
internet. The proxy must set the `X-Forwarded-Proto` header so that the
session cookie is marked secure.

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

Status page subscriptions, alert message templates, the REST API, the config
file mode and checks from more than one place. [ROADMAP.md](ROADMAP.md) has
the full list and the order.

## License

MIT. See [LICENSE](LICENSE).
