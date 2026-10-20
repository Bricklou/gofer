# Security

Gofer holds the keys to your email, so I take security reports seriously. This page explains how to report a problem, what Gofer protects, and what it doesn't.

## reporting a vulnerability

If you found something that could be used to attack the system, please try to avoid opening a public issue. Instead, use GitHub's [private vulnerability reporting](https://github.com/cristianadrielbraun/gofer/security/advisories/new) for this repository. That way we keep the vulnerability hidden from bad guys ;)

Include what you found, how to reproduce it, and which version you tested. I'll reply as soon as I can, usually within a few days. Gofer is a one-person project, so please give me a reasonable amount of time to fix the issue before you publish details. I'll credit you in the release notes unless you'd rather I didn't.

For general security questions, bugs, ideas, requests, a regular issue is fine

## supported versions

Gofer is pre-1.0. Security fixes go into the latest release only, so please upgrade before reporting.

## how Gofer protects your data

**Stored credentials.** IMAP, SMTP, CardDAV, and CalDAV passwords and OAuth tokens for Gmail and Outlook are encrypted with AES-256-GCM. Each one is bound to the user, account, and service it belongs to, so a value copied into another row won't decrypt there. The key is `data/secret.key`, generated on first start, or `GOFER_SECRET_KEY` if you set it.

**Gofer logins.** Passwords are hashed with Argon2id and checked against a list of common passwords. Login attempts are throttled. MFA is available with passkeys (WebAuthn) or TOTP, and in managed mode admins can require it. Session cookies are `HttpOnly` and `SameSite`, and state-changing requests are protected against CSRF.

**Reading mail.** Message bodies are rendered in a sandboxed iframe under a strict Content Security Policy: scripts in the message don't run, forms can't submit, and nothing loads from outside Gofer. Remote images are blocked by default. When you allow them, Gofer downloads them on the server and serves them from its own address, so the sender doesn't see your browser. Links to attachments and inline images are signed and only work for their own message.

**Outgoing connections.** Gofer refuses to connect to private network addresses unless an admin approves them. This stops a malicious account or calendar URL from reaching other services on your network.

**Separation between users.** In managed mode, each user's mail, contacts, and calendar live in their own SQLite database. A Gofer login session is never accepted as a mailbox credential, and connecting a mailbox never signs anyone into Gofer.

## what Gofer doesn't protect against

- **Open mode has no login.** The default `GOFER_AUTH_MODE=open` is meant for local use only. Anyone who can reach the port can read your mail. Use `personal` or `managed` mode for anything reachable from another machine.
- **Access to the data directory.** By default, `secret.key` sits next to the databases. Anyone who can read `data/` can decrypt your stored credentials. Set `GOFER_SECRET_KEY` to keep the key out of that directory, and protect your backups as carefully as the server.
- **A compromised server.** Gofer needs to decrypt your credentials to sync mail, so someone with control of the running process or host can get them too.
- **Plain HTTP.** Serve Gofer over HTTPS through a reverse proxy. `GOFER_ALLOW_INSECURE_LAN=true` sends session cookies unencrypted on your local network. Only use it if you trust that network.

## disclosure

Gofer hasn't had an independent security audit. It has a large test suite, and I use it every day for my own mail, but it's pre-1.0 software built by one person with a lot of help from AI coding tools. I review and test what goes in, but that isn't the same as an expert security review. Keep that in mind before you expose it to the internet, and keep backups.

Deployment advice (reverse proxy, trusted proxies, allowed networks) is in [docs/setup.md](./docs/setup.md#deployment-precautions).
