# Trino Manual QA Checklist

## Setup

- [ ] Save Trino settings: host, catalog, schema, user, source.
- [ ] Set Trino auth secret for selected mode (`basic` password or `jwt` token), or `none`.
- [ ] Confirm `/api/trino/status` reports expected mode and config flags.

## Connection Test + Enablement

- [ ] Run Trino connection test from settings UI.
- [ ] Verify success updates datasource validation status and timestamp.
- [ ] Verify enabling Trino SQL tools is blocked before successful validation.
- [ ] Verify enabling succeeds after successful validation.

## Query Execution

- [ ] Run `run_trino_query` with a small read-only query and verify rows/schema metadata.
- [ ] Verify `preflight_failed` flow returns rewrite guidance for invalid read-only SQL.
- [ ] Verify metadata tools return expected structures:
  - `list_trino_catalogs`
  - `list_trino_schemas`
  - `list_trino_tables`
  - `describe_trino_table`

## Async Lifecycle

- [ ] Submit a long-running query and verify running envelope includes `job_id` + `owner_token`.
- [ ] Poll with `check_query_status` until completion.
- [ ] Fetch rows with `get_query_results` (including pagination).
- [ ] Verify query metadata contains Trino provider identifiers and URL fields.

## Cancel

- [ ] Submit a long-running query and cancel with `cancel_query`.
- [ ] Verify job transitions to cancelled and returns user-facing cancellation response.
- [ ] Verify backend attempts Trino cancel via current URI and fallback query endpoint.
