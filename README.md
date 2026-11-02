# beléptető.lite

A minimal, single-binary door access system. One Go process embeds an MQTT
broker, serves an HTMX web UI, and stores everything in a local SQLite file.
It is a lightweight alternative to the full beléptető backend, implementing
just card reads, door open/deny, and card pairing for a single door.

## What it does

- **Admins** register and deactivate users and review access logs.
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
GOOS=linux GOARCH=arm64 go build -o belepteto-arm64 .
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

## Connecting a controller

The controller communicates over MQTT under `controller/{id}/...`, where `{id}`
matches `BELEPTETO_CONTROLLER_ID`. This build uses the following subset of the
protocol:

- **Subscribes** to `controller/{id}/cardRead` — card scans reported by the
  reader (a batch of cards with a correlation `id`).
- **Publishes** to `controller/{id}/doorControl` — plaintext `open <id>`,
  `deny <id>`, or `ignore <id>`, echoing the cardRead id.
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
   and other formatting are significant. The card is stored in uppercase
   colon-hex form.
3. If any scanned card belongs to an active card owned by an active user, the
   server publishes `open <id>` and records an **entry**. Otherwise it publishes
   `deny <id>`. Every scan is recorded as a **card read**.

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
backend healthcheck, OAuth login, and mutual-TLS/ACL on the broker.
