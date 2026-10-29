# LinkSphere branding and compatibility

User-facing product names, service display descriptions, and setup guidance use
**LinkSphere**. Legacy identifiers below remain intentional compatibility names
until an explicit migration with aliases and upgrade tests is implemented:

- Existing executable and service identifiers such as `gosshd-agent` and
  `gosshd-server`. Changing a displayed service description does not rename the service.
- Published release asset filenames. Documentation must link to files that actually exist.
- Android application ID `com.gosshd.desktop`, component names and native-library names.
  A package-ID rename would otherwise create a different application and lose existing grants.
- Existing `GOSSHD_*` environment variables, pairing/data directories, configuration keys,
  protocol messages and browser storage keys. These must not be silently replaced.
- Historical migration references and third-party copyright/license notices.

A future identifier migration must introduce the preferred LinkSphere alias,
keep old values readable, define conflict precedence, preserve existing settings,
and test upgrades before removing any legacy alias. Branding cleanup does not
authorize new device permissions or changes to enrollment credentials.

## Preferred environment names

The agent accepts `LINKSPHERE_SSH_HOST`, `LINKSPHERE_SSH_PORT`,
`LINKSPHERE_DESKTOP_ENABLE`, `LINKSPHERE_DESKTOP_DISABLE`, and
`LINKSPHERE_DESKTOP_HELPER`. Their existing `GOSSHD_` equivalents remain accepted.
A nonblank preferred value takes precedence; an empty or whitespace-only value
falls back to the legacy variable. Explicit SSH host/port settings keep priority over environment hints. Desktop
enable retains its existing environment override behavior; explicit desktop
disable still wins. For desktop disabling, a true value in **either** prefix wins.
An explicit desktop-disable opt-out is preserved across either prefix.

The server accepts `LINKSPHERE_BOOTSTRAP_ADMIN_PASSWORD` and
`LINKSPHERE_SECRET_KEY` with legacy aliases. Unlike ordinary options, differing
nonblank secret-key aliases produce an error instead of silently selecting a new
encryption key. Explicit key configuration and explicit key-file configuration
retain their existing priority. Existing generated key files are not renamed.

Release launchers accept `LINKSPHERE_REPO`, `LINKSPHERE_VERSION`, and
`LINKSPHERE_PROXY_URL` with their legacy equivalents. The POSIX launcher also
accepts `LINKSPHERE_TMPDIR`. Published asset names and existing installation
locations remain unchanged so an upgrade can still find the old agent.
