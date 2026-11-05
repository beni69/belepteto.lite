# beléptető.lite

A minimal, single-binary door access system. One Go process embeds an MQTT
broker, serves an HTMX web UI, and stores everything in a local SQLite file.
It is a lightweight alternative to the full beléptető backend, implementing
just card reads, door open/deny, and card pairing for a single door.

## What it does

- **Admins** register and deactivate users and review access logs.
- **Admins** can set people to always-allowed or time-limited entry, and manage
  multiple scheduled entry windows.
- **Users** pair cards to themselves from the web UI and manage their labels.
- A **door controller** (cardreader hardware) connects over MQTT. When a card
  is read, the server decides open or deny and replies to the controller. All
  access decisions live in the server; the device is stateless.

## Stack

- Go, single statically-linkable binary (no cgo).
- Routing: [chi](https://github.com/go-chi/chi).
- Database: SQLite via [GORM](https://gorm.io) with the pure-Go
  [glebarez/sqlite](https://github.com/glebarez/sqlite) driver.
- Embedded MQTT broker: [mochi-mqtt](https://github.com/mochi-mqtt/server).
- Frontend: Go `html/template` + [HTMX 4](https://htmx.org) +
  [Pico CSS](https://picocss.com), all embedded in the binary.

## Build & run

```sh
make build        # -> bin/belepteto
make run          # run from source
make test         # run the test suite
```

The whole app — broker, web server, templates, CSS, and JS — is one binary with
no external assets. Because the SQLite driver is pure Go, cross-compiling is
just a matter of `GOOS`/`GOARCH`:

```sh
make build-static GOOS=linux GOARCH=arm64
```

On first start with an empty database, an initial admin is created. If you did
not set `BELEPTETO_ADMIN_PASSWORD`, a random password is generated and printed
to the log **once** — save it.

## Configuration

All configuration is via environment variables:

| Variable | Default | Purpose |
| --- | --- | --- |
| `BELEPTETO_HTTP_ADDR` | `:8080` | Web UI / API listen address |
| `BELEPTETO_MQTT_ADDR` | `:1883` | MQTT broker listen address |
| `BELEPTETO_DB_PATH` | `belepteto.db` | SQLite database file |
| `BELEPTETO_CONTROLLER_ID` | `1619` | ID of the door controller this instance talks to |
| `BELEPTETO_ADMIN_EMAIL` | `admin@example.com` | Email for the seeded admin |
| `BELEPTETO_ADMIN_PASSWORD` | _(random)_ | Password for the seeded admin; random if unset |
| `EXTERNAL_API_ACCESS_TOKEN` | _(disabled)_ | Bearer token required by the external access API |
| `ISSUER_URL` | _(unset)_ | OIDC issuer URL; may be used instead of `WELL_KNOWN_URL` |
| `WELL_KNOWN_URL` | _(unset)_ | Explicit OIDC discovery document URL; may be used instead of `ISSUER_URL` |
| `CLIENT_ID` | _(unset)_ | Manually registered OIDC client ID |
| `CLIENT_SECRET` | _(unset)_ | Manually registered OIDC client secret |
| `SCOPES` | _(unset)_ | Space-separated OIDC scopes; must include `openid email` |
| `SSO_REDIRECT_URL` | _(unset)_ | Exact callback URL registered with the identity provider |
| `SSO_FRONTEND_URL` | _(unset)_ | Public frontend origin; successful SSO returns to its `/` path |
| `LOGIN_BUTTON_TEXT` | `Bejelentkezés SSO-val` | Optional text for the SSO login button |

The external API is disabled when `EXTERNAL_API_ACCESS_TOKEN` is empty. Set a
long, random secret and send it in the `Authorization: Bearer <token>` header.
Do not put the token in a URL or request body. Expose the API over HTTPS.

SSO is disabled when none of its core settings are set. If any are set, the
configuration must be complete; the application reports an error on startup for
partial or invalid configuration. Configure an OIDC client manually with the
authorization-code grant and `RS256` ID-token signing. `SSO_REDIRECT_URL` must
match the registered callback exactly, and it must share an origin with
`SSO_FRONTEND_URL` so the session cookie remains available after the redirect.
When both discovery variables are set, the document at `WELL_KNOWN_URL` is used
and its issuer must exactly match `ISSUER_URL`. If only `ISSUER_URL` is set, the
standard `/.well-known/openid-configuration` path is used.

SSO only links an identity to an existing active local account; it does not
create users. The first login requires a verified email claim matching that
account. Later logins use the issuer and OIDC `sub` saved on the local user.

## Connecting a controller

The controller communicates over MQTT under `controller/{id}/...`, where `{id}`
matches `BELEPTETO_CONTROLLER_ID`. This build uses the following subset of the
protocol:

- **Subscribes** to `controller/{id}/cardRead` — card scans reported by the
  reader (a batch of cards with a correlation `id`).
- **Publishes** to `controller/{id}/doorControl` — plaintext `open <id>`,
  `deny <id>`, or `ignore <id>` for card reads (echoing the cardRead id), plus
  plain `open` for authorized remote opens from the signed-in web UI.
- **Publishes** to `controller/{id}/command` — `alternativeColorOn` /
  `alternativeColorOff` to signal pairing mode on the reader.

Point the controller's MQTT client at `BELEPTETO_MQTT_ADDR`. The broker
currently accepts all connections (plaintext). Mutual TLS with client
certificates and topic ACLs are planned; the broker's listener config and auth
hook are where they will plug in.

### Testing without hardware

Any MQTT client can stand in for the controller. For example, publish a card
read and watch the door command come back:

```sh
# subscribe to the controller's door commands
mosquitto_sub -h localhost -p 1883 -t 'controller/1619/doorControl'

# in another shell, simulate a card scan
mosquitto_pub -h localhost -p 1883 -t 'controller/1619/cardRead' \
  -m '{"cards":[{"cardID":"AA:BB:CC:DD:00:00:00:00"}],"id":42,"timestamp":0}'
```

An unregistered card yields `deny 42`; a card paired to an active user yields
`open 42`.

## How access works

1. The reader publishes a `cardRead` (one or more cards, a correlation `id`).
2. The server uppercases each UID and looks it up exactly as received otherwise.
   Reader UIDs are expected in colon-separated hexadecimal form, so separators
   and other formatting are significant. The normalized UID is stored as a
   deterministic SHA-256 hash. The first two normalized characters are stored
   separately so the frontend can identify a card without receiving the full
   UID or its hash.
3. If any scanned card belongs to an active card owned by an active user, the
   server checks that user's door access. Always-allowed users can enter at any
   time; time-limited users need a current entry window (`valid_from <= now <
   valid_until`). The server then publishes `open <id>` and records an **entry**,
   or publishes `deny <id>`. Every scan is recorded as a **card read**.
4. Signed-in users with current door access can also use the floating remote-open
   control. It requires a two-press confirmation, and the server checks access
   again immediately before publishing the plain `open` command. The web log
   records the request as a remote entry; the success message confirms that the
   broker accepted the request, not that the controller physically opened the
   door.

Admins can expand each person's card list to rename a card or unpair it. Unpairing
removes that card assignment, so the card can be paired to another user later;
the stored UID hash is never shown in the UI.

## Time-limited entry

Admins can add several independent entry windows for one person from the Admin
page. Existing users remain always allowed unless an admin changes their
setting to **Only during listed times**. Adding a time window does not change
that setting. Windows use the server's local time in the web form; the API uses
RFC 3339 timestamps with an explicit timezone. The end of a window is exclusive.
When registering a user, admins can choose always-allowed or scheduled-only
access; always-allowed is selected by default.

### External API

`PUT /api/v1/access-grant-sets` replaces the complete desired list for one
generic source/scope pair. It is intended for systems that may retry or miss
updates: each `source`/`scope_ref` pair has a monotonically increasing
`revision`, and an omitted entry is revoked. The full update is validated and
applied atomically. All emails must match active, existing users; this API does
not create people or accounts.
The API does not change a person's always-allowed setting. The response calls
out users who already have always-allowed access.
The sending system should retry until it receives a successful response and
periodically resend its latest full snapshot; replaying the same revision and
content is safe and reconciles the saved list.

```sh
curl -X PUT http://localhost:8080/api/v1/access-grant-sets \
  -H 'Authorization: Bearer YOUR_EXTERNAL_API_ACCESS_TOKEN' \
  -H 'Content-Type: application/json' \
  -d '{
    "source": "booking-system",
    "scope_ref": "access-list-123",
    "revision": 1,
    "grants": [
      {
        "external_ref": "registration-456",
        "email": "person@example.com",
        "valid_from": "2026-11-10T17:00:00+01:00",
        "valid_until": "2026-11-10T22:00:00+01:00"
      }
    ]
  }'
```

Retry the same revision with the same content safely. Reusing a revision with
different content or sending an older revision returns `409 Conflict`. To
remove every entry in a scope, send a newer revision with an empty `grants`
array. Read the last applied state with
`GET /api/v1/access-grant-sets?source=booking-system&scope_ref=access-list-123`,
using the same bearer header. A successful retry reports
`"revision_advanced": false`; this does not prevent the server from reconciling
the snapshot again.

## Card pairing

Users add cards by pairing, not by typing a UID:

1. The user clicks **Pair a card**. The server enters pairing mode for that user
   and tells the reader to show its alternative color.
2. The next card scan is treated as an assignment, not an entry: the card is
   assigned to that user and the reader is told to `ignore` the scan (no open,
   no deny).
3. Pairing is a single global session — while one user is pairing, others are
   told the reader is busy. It times out after 10 seconds if no card is
   presented.

## Not implemented (by design)

This is the "lite" build. The following parts of the full protocol/system are
intentionally left out and may be added later: remote (button) door opening,
heartbeat/syslog/doorState telemetry, controller DB sync (`dbControl`/`dbPush`),
and mutual-TLS/ACL on the broker.
