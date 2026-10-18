# Converting to per-user storage

Since 0.5.0, managed mode keeps each user's data in a separate database. An installation that started before 0.5.0 in managed mode, or an open or personal installation switched to managed mode, is converted on its first start in managed mode. This page covers how that conversion works. Back to [setup](./setup.md).

An existing shared database is converted automatically the first time Gofer starts in managed mode, keeping the same `GOFER_DB_PATH`. Conversion can take a while for large mailboxes; the server starts listening once it finishes. The original database is kept as `<database>.shared` next to the new central database, its `.users` directory and `.layout.json` file. Back up the new files together with the encryption key and the `accounts` directory. If conversion fails, the original database is put back unchanged and the error is logged; if it is interrupted, it resumes on the next start. When the failure comes from the database's contents, later starts report the same error immediately instead of repeating the conversion, until the database or Gofer changes; delete `<database>.migration-failed.json` to retry anyway. Personal and open modes keep shared storage.

> [!NOTE]
> **Staying in open or personal mode?** Nothing changes for you in 0.5.0. Your database receives the usual automatic schema upgrade on the first start, and the per-user conversion below never runs unless you switch to managed mode.

> [!WARNING]
> **Upgrading a managed installation from a version before 0.5.0, or switching an existing installation to managed mode.** The first start in managed mode converts your database to per-user storage, and this is one-way:
>
> - **Choose when it happens.** The conversion runs automatically and may take several minutes. If Gofer can restart without you, for example with automatic container updates, set `GOFER_PREVENT_STORAGE_CONVERSION=true` before upgrading. Managed mode then refuses to start with an explanation instead of converting, and leaves the database untouched; remove the setting when you are ready. It does not stop a conversion that was already interrupted from finishing.
> - **Back up first.** Stop Gofer and copy the whole data directory, including the database, `secret.key` (unless you use `GOFER_SECRET_KEY`) and the `accounts` directory.
> - **Have free disk space for a second copy.** The conversion needs about twice the database size: the original is kept as `<database>.shared` and the new databases are roughly as large, plus temporary files while it runs.
> - **Start from the same working directory as before.** When `GOFER_DB_PATH` is relative, earlier versions stored message and attachment file paths relative to the directory Gofer was started from. The conversion resolves them from the current directory; started elsewhere, it stops and leaves the database unchanged. After the first successful start, the paths are absolute and any working directory works.
> - **There is no way back except the retained original.** After conversion, earlier versions and personal mode cannot open the data. To return to the pre-upgrade state, stop Gofer, move away `<database>`, `<database>.users` and `<database>.layout.json`, and rename `<database>.shared` back to `<database>`. Mail, settings and accounts changed since the conversion are lost, and you need a pre-0.5.0 release to run it. Once you are sure you won't roll back, you can delete `<database>.shared` to free its space.
> - **Switching from open or personal mode works the same way.** Your profile keeps its mailboxes and becomes a regular user, and setup creates a separate administrator account. A personal profile keeps its password and other sign-in methods. An open profile has none yet, so the setup notice in the terminal also shows a token for setting its password at `/account/enroll`.

To convert into a separate file instead, stop Gofer and run `./gofer storage migrate --db data/gofer.db --to data/gofer-users.db` (repeat with `--retry` if interrupted), then set `GOFER_DB_PATH=data/gofer-users.db`.

