# uppies

A small self-hosted uptime monitor with incident management, a timeline and
public status pages for several projects from one install.

Status: early. The core works and is tested, but it has not run in production
and several planned features are missing (see "Not built yet").

## What it does today

- Checks HTTP(S) URLs and TCP ports on a schedule you set per monitor.
- Waits for retries before it calls something down, and for good checks before
  it calls it up again, so one failed request does not raise an alarm.
- Serves one public status page per project at `/s/<address>`, with a 90-day
  uptime bar per monitor. The pages are plain HTML and work without JavaScript.
- Opens a private draft incident when a monitor goes down. You decide whether
  to publish it. Published incidents carry dated updates with a status
  (investigating, identified, monitoring, resolved).
- Keeps a timeline per project of every outage, recovery and incident update.
- Sends an alert when a monitor goes down and when it recovers, to a plain
  webhook (JSON), Discord or Slack. The alert says what failed, why, and on
  recovery for how long.
- Lets you shape each status page: tagline, link to your homepage, accent
  colour, light or dark theme, 30, 60 or 90 days of history, your own CSS, and
  which monitors are shown.
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

Put a reverse proxy with TLS in front of uppies before you expose it to the
internet. The proxy must set the `X-Forwarded-Proto` header so that the
session cookie is marked secure.

## Develop

```sh
go build ./... && go vet ./... && go test -race ./...
```

`PROGRESS.md` records what is done, what is verified and what comes next.

## Not built yet

Email alerts, scheduled maintenance, the config file mode, the REST API, ping,
DNS and push monitors, certificate expiry warnings, custom domains and logos
for status pages, and a Docker image.

## License

MIT. See [LICENSE](LICENSE).
