<h1>
  <img src="./assets/logo.svg" width="64" align="absmiddle" alt="Gofer logo" />
  Gofer
</h1>

<img alt="Gofer's inbox in its three styles, side by side: Classic in dark mode, Minimal, and Express" src="./screenshots/themes.png" />
<p align="center">Classic, Minimal, and Express, left to right</p>

[View all screenshots](./screenshots/README.md)

<br>

Gofer is a self-hosted webmail I'm building because I wanted one that's actually nice to use. Mail, contacts, and calendar in one place, running on your own machine or server, open in any browser.

It's pre-1.0, so things still change, but I use it every day with six accounts and around 100k emails, and it holds up fine.

## what it does

- **All your accounts in one place:** Gmail, Outlook, iCloud, and any IMAP/SMTP server.
- **Mail the way you'd expect:** threads, labels, drag and drop, scheduled send, signatures, translation, and solid search.
- **Contacts and calendar too:** syncs with Google, Outlook, CardDAV, and CalDAV. Drag events around, send invites, RSVP right from the email, and add Meet or Teams links.
- **Your data stays yours:** export mailboxes as mbox, import from other apps, move mail between accounts, and take your whole setup to another Gofer.
- **For you, your family, or your office:** run it with no login, as one protected profile, or for several users with their own admins.
- **Safe by default:** encrypted credentials, remote images blocked, and MFA with passkeys or TOTP.
- **Make it yours:** three looks (Classic, Minimal, and a retro one called Express), flexible layouts, and push notifications.

## try it

Grab a binary from the [releases page](https://github.com/cristianadrielbraun/gofer/releases), run it, and open `http://localhost:8090`. IMAP accounts work right away. Gmail and Outlook need your own OAuth app for now (I'm working on that).

Auth modes, accounts, upgrades, and deployment are all in [docs/setup.md](./docs/setup.md). Please keep backups before upgrading.

To build from source you need Go 1.27.1+, `templ`, `tailwindcss`, and `task`:

```sh
task dev      # dev server with hot reload, at http://local.localhost:8090
task release  # self-contained binary at ./dist/gofer
```

## what's next

Easier first-run setup, Gmail and Outlook without your own OAuth app, clearer reconnect flows, more calendar providers, more shortcuts and bulk actions. Ideas and bug reports are welcome.

## built with

I build Gofer with a lot of help from AI coding tools. I decide what goes in, review it, and use the result every day.

Some of the main libraries and tools Gofer leans on, because pretending I wrote the whole mail stack from scratch would be absurd:

- [templ](https://templ.guide/) for Go-based views
- [templUI](https://templui.io/) for several UI components
- [HTMX](https://htmx.org/) for server-driven interactions
- [Tailwind CSS](https://tailwindcss.com/) for styling
- [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) for SQLite storage
- [emersion](https://github.com/emersion)'s Go mail libraries, including [go-imap](https://github.com/emersion/go-imap), [go-smtp](https://github.com/emersion/go-smtp), [go-message](https://github.com/emersion/go-message), [go-sasl](https://github.com/emersion/go-sasl), and [go-vcard](https://github.com/emersion/go-vcard), which provide much of Gofer's mail, MIME, auth, and contact-format foundation
- [golang.org/x/oauth2](https://pkg.go.dev/golang.org/x/oauth2) for OAuth flows
- [webpush-go](https://github.com/SherClockHolmes/webpush-go) for Web Push notifications
- [Lucide](https://lucide.dev/) icons through templUI's icon component
