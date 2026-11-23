# Roadmap

This is the planned order of work. It is a plan, not a promise, and it has no
dates. The order comes from three questions asked of other uptime monitors:
what their users complain about, what their users ask to customize, and what
paid services include as standard.

`PROGRESS.md` says what is already built and verified.

## Done

- HTTP(S) and TCP checks with retries before down and good checks before up.
- Status pages for several projects from one install, with a 90-day bar.
- Incidents with dated updates, and private drafts opened by outages.
- A timeline per project.
- DNS and ping checks, push monitors for scheduled jobs, and certificate
  expiry warnings.
- Alerts by email and to a webhook, Discord or Slack, and an outside heartbeat.
- Planned maintenance windows.
- A Dockerfile.
- Status page settings: logo, favicon, tagline, homepage link, theme, accent
  colour, days of history, timezone, footer, custom CSS, custom domain, hidden
  monitors, no vendor name, search engine opt-out, and a badge.

## Before the next features

The look and layout of the status page and the admin pages get a design pass
first.

## 1. Telling people what is happening

0. **Release builds**, and a published container image, so that installing
   takes one command.
1. **Status page subscriptions.** An Atom feed first, since it needs no mail
   server. Email subscriptions after email alerts exist.
2. **More history and ranges.** 24 hours, 7 days and 30 days next to the daily
   bar, and up to 365 days. This needs hourly totals.
3. **Alert message templates.** Changing what an alert says is the most
   requested customization in other monitors.
4. **A password on a status page**, for pages meant for a team or a customer.
5. **Order and groups on the status page.** Monitors are listed by name today.
6. **Documented class names for custom CSS**, kept stable between releases, so
   that a custom style survives an upgrade.
7. **Alert quality.** Repeat an alert while something stays down, and tell an
   outage of everything apart from uppies losing its own network, once there is
   an independent reference to check against.

## 2. Automation

1. **A REST API with keys** that covers everything the admin pages do. A
   missing API is the most upvoted complaint about the most used open source
   monitor.
2. **A config file** for monitors, projects and alert channels, kept in git.
   Items from the file are read-only in the admin pages.
3. **Import from Uptime Kuma.**

## 3. Growing with a team

1. **Checks from more than one place.** A small probe runs checks elsewhere
   and reports back, and a failure counts only when a second place agrees.
2. **More than one user**, two-factor sign-in, then single sign-on.
3. **PostgreSQL** as a second database for people who already run one. SQLite
   stays the default.
4. **Other languages** for the status page.
5. **Dependencies between monitors**, so that one root failure sends one alert.

## Not planned

Each of these would add a large moving part, and other tools do them well.

- SMS and phone calls. They need a paid carrier account. A webhook to a
  service you already use covers it.
- Checks that drive a real browser.
- On-call schedules and escalation chains.
- Server metrics such as CPU, memory and disk.
- Page speed scores and broken-link crawling.
- Custom scripts and tracking codes on status pages. They would mean giving
  up the rule that a status page runs no script at all.
