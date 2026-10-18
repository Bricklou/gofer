# Setting up Gofer

Everything about configuring, upgrading, and deploying Gofer. For a quick start, see the [README](../README.md).

## auth modes and storage

Choose `GOFER_AUTH_MODE=managed` for multiple users with separate administrator accounts, or `personal` for one protected profile with multiple mailboxes. Both modes guide you through first-run setup using a token printed in the terminal. The default `open` mode has no login and is intended for local use. See [`.env.example`](../.env.example) for configuration options.

Managed mode keeps each user's mail, contacts, Calendar and preferences in a separate SQLite database. Authentication and installation settings stay in the central database. New installations initialize this layout automatically.

Upgrading a managed installation from before 0.5.0, or switching an existing open or personal installation to managed mode? Gofer converts the database on the first start, one way only. Read [pre-0.5.0-migration.md](./pre-0.5.0-migration.md) and back up first.

## accounts

Generic IMAP/SMTP accounts need no OAuth application credentials. Gmail and Outlook currently require your own provider client ID and secret.

iCloud is an IMAP/SMTP account with Apple's servers filled in. Apple doesn't let other mail apps sign in with your Apple Account password, so create an app-specific password at [account.apple.com](https://account.apple.com) under **Sign-In and Security** (this needs two-factor authentication) and enter it when adding the account. Contacts and Calendar use the same login through CardDAV and CalDAV. Changing your Apple Account password revokes app-specific passwords, so you'll need to create a new one and enter it under **Edit account**. iCloud+ custom-domain addresses should work by signing in with your @icloud.com address, but haven't been tested yet. Mailbox authorization and optional Google/Microsoft application sign-in use separate clients and callbacks.

> [!NOTE]
> I plan to add Gmail and Outlook connections that don't require you to provide your own OAuth app credentials. First, I need to research the right way to set up public OAuth applications and complete the providers' identity verification and any required app reviews. Until then, you'll need to use your own credentials as described above.

Configure Calendar under **Settings → Accounts**: enable it for an account, discover calendars, and select which ones to sync. Google and Outlook use the account's existing OAuth connection; reconnect older accounts to grant Calendar access if needed. Generic accounts can connect a CalDAV server using an HTTPS URL and server credentials, which may require an app password. iCloud accounts connect to iCloud's CalDAV server with the account's app-specific password.

## deployment precautions

For server deployments, use personal or managed authentication, serve the application through an HTTPS reverse proxy, and configure `GOFER_ADDR` and `GOFER_BASE_URL` for your deployment. The listener binds to loopback by default. Set `GOFER_TRUSTED_PROXY_CIDRS` only for your actual proxy peers; use `GOFER_ALLOWED_CIDRS` when you need to restrict client networks.

To sign in over plain HTTP on a home or office network, set `GOFER_BASE_URL` to the server's private address and `GOFER_ALLOW_INSECURE_LAN=true`. Sign-in cookies then travel unencrypted on that network, and Gofer logs a warning at startup; public addresses and host names still require HTTPS.

Gofer doesn't connect to servers on private network addresses unless they are approved. If your mail, calendar, or contacts server is on your local network, approve it under Mail security: in Settings, Security for personal and open installations, or in the admin panel in managed mode.

Runtime data is stored in `data/` by default. Protect the data directory, configuration, and secrets, and back them up together before upgrades. Shared databases apply schema migrations at startup, and managed mode converts them to per-user storage as described in [pre-0.5.0-migration.md](./pre-0.5.0-migration.md). Going back to an older version may require restoring its matching backup, and the retained shared database does not include changes made after conversion. I recommend testing your providers and access setup before relying on Gofer for anything critical.

## admin panel

In managed mode, `/admin` provides user administration, invitations, security policies, and security activity. It also includes diagnostics for account sync, contacts, avatars, and provider behavior.

