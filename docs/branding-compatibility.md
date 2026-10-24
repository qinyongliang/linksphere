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
