# Trino Integration Plan (Contract Freeze)

## Settings Contract

Trino runtime settings are stored in `/api/settings` under these fields only:

- `trinoHost`
- `trinoCatalog`
- `trinoSchema`
- `trinoUser`
- `trinoSource`

No Trino auth secrets are persisted in settings.

## Secrets Contract

Trino auth material is stored in encrypted secrets storage and managed via:

- `POST /api/secrets/set` with:
  - `providerId: "trino"`
  - `authType: "basic" | "jwt" | "none"`
  - `basic` requires `username` and `password`
  - `jwt` requires `jwt` (or `apiKey` fallback)
  - `none` clears auth headers while keeping non-secret settings
- `POST /api/secrets/clear` with:
  - `providerId: "trino"`
  - `authType: "basic" | "jwt" | "all"`

## Trino API Routes

Mounted under `/api/trino/*`:

- `GET /api/trino/status`
- `POST /api/trino/test`
- `POST /api/trino/preflight`
- `POST /api/trino/query`
- `GET /api/trino/catalogs`
- `GET /api/trino/schemas`
- `GET /api/trino/tables`
- `GET /api/trino/table-schema`

## Auth and User Semantics

- `basic`: `X-Trino-User` is sourced from encrypted Trino basic auth username.
- `jwt`: `X-Trino-User` is sourced from `settings.trinoUser`.
- `none`: `X-Trino-User` is sourced from `settings.trinoUser`.

## Transport Policy

- `basic` and `jwt` modes require `https://` Trino hosts.
- `none` mode allows `http://` and `https://` (for local/dev support).
