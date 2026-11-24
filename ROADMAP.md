# Roadmap

## Shipped

- Uppies checks HTTP(S), TCP, DNS and ping targets and watches scheduled jobs.
  It warns before HTTPS certificates expire and sends alerts by email, webhook,
  Discord or Slack. An outbound heartbeat lets another service check that
  Uppies is running.
- The same monitor can appear on several status pages, each with its own
  monitor order, branding and domain settings. A page can show daily uptime
  over 7 to 365 days. The homepage shows uptime and recent incidents across
  pages.
- Published incidents have dated updates and a public archive. Maintenance
  can cover selected monitors, carry public updates and stay open until you
  end it.
- The admin supports multiple accounts. [API v1](API.md) uses scoped tokens.
- Docker images are available for Linux on amd64 and arm64. SQLite is the
  default database. Uppies also supports PostgreSQL, MariaDB, embedded Turso
  SQLite and Turso PostgreSQL.

## Next

1. Publish standalone binaries.
2. Publish an Atom feed, then add email subscriptions.
3. Add 24-hour, 7-day and 30-day uptime views using hourly totals.
4. Let users set alert message templates.
5. Add password protection for status pages.
6. Document a stable set of CSS classes for custom styles.
7. Repeat alerts during an outage and add an independent check for Uppies'
   own network.

## Automation

1. Extend [API v1](API.md) to cover more admin tasks.
2. Read monitors, status pages and alert channels from a config file kept
   in Git. Entries managed by the file are read-only in the admin.
3. Add an import for Uptime Kuma.

## Teams and checks

1. Run checks from more than one location. Require a second location to
   confirm a failure.
2. Add two-factor sign-in, then single sign-on.
3. Translate the public status pages.
4. Use monitor dependencies so a shared failure sends one alert.

## Out of scope

- SMS and phone calls.
- Browser checks, page speed scores and broken-link crawling.
- On-call schedules and escalation chains.
- Host metrics such as CPU, memory and disk.
- Custom scripts and tracking codes on status pages.
