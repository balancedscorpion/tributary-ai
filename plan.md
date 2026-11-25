# Databricks (GCP) PAT/Refresh Integration Plan

## Summary

Implement Databricks support for **GCP-hosted workspaces only**, with auth via either:
- Personal Access Token (PAT)
- Refresh token (with access-token exchange + rotation handling)

All Databricks API traffic remains server-side through the existing Go backend pattern (same as BigQuery).

## Scope and Non-Goals

- Scope: satisfy the requirement from `docs/databricks.md` for Databricks on GCP with PAT or refresh token.
- Non-goal: UI tab restructuring. We will **not** add/migrate a new "Data Sources" tab in this change.

## Explicit Secret API Contract

Frontend and backend must use a single contract for Databricks secrets.

### Set secret payloads

- Refresh token:
```json
{
  "providerId": "databricks",
  "authType": "refresh_token",
  "refreshToken": "doau..."
}
```

- PAT:
```json
{
  "providerId": "databricks",
  "authType": "pat",
  "apiKey": "dapi..."
}
```

### Clear secret payloads

- Clear refresh token only: `{ "providerId": "databricks", "authType": "refresh_token" }`
- Clear PAT only: `{ "providerId": "databricks", "authType": "pat" }`
- Clear all Databricks auth: `{ "providerId": "databricks", "authType": "all" }`

### Validation rules

- `authType` is required for `providerId=databricks`.
- `refresh_token` requires non-empty `refreshToken`.
- `pat` requires non-empty `apiKey`.
- Invalid combinations return HTTP 400 with explicit error messages.

---

## Files to Create

### 1. `server/internal/databricks/handler.go` (new)
Databricks HTTP handler implementing `http.Handler`, mirroring `server/internal/bigquery/handler.go` patterns.

**Routes served:**
| Route | Method | Purpose |
|-------|--------|---------|
| `/api/databricks/status` | GET | Auth mode + host/warehouse config status |
| `/api/databricks/test` | POST | Connectivity test against configured warehouse |
| `/api/databricks/preflight` | POST | Read-only guard preflight (no statement execution) |
| `/api/databricks/query` | POST | Execute SQL via Statement Execution API |
| `/api/databricks/catalogs` | GET | List Unity Catalog catalogs |
| `/api/databricks/schemas` | GET | List schemas (`?catalog_name=X`) |
| `/api/databricks/tables` | GET | List tables (`?catalog_name=X&schema_name=Y`) |
| `/api/databricks/table-schema` | GET | Describe table (`?full_name=catalog.schema.table`) |

**Key details:**
- `doDatabricks()` helper obtains a token, calls `https://{host}/api/...`, retries once on 401 after cache invalidation.
- `query` uses `/api/2.0/sql/statements` with `wait_timeout: "30s"`, `disposition: "INLINE"`, `format: "JSON_ARRAY"`, and polling for `PENDING`/`RUNNING`.
- **Read-only safety parity with BigQuery**:
  - `preflight` and `query` both enforce read-only SQL (`SELECT`/`WITH` only, single statement, banned write/admin verbs).
  - `query` rejects non-read-only SQL with 400 before any upstream execution.
- Host validation is enforced before any upstream call (see GCP rules below).

### 2. `server/internal/databricks/auth.go` (new)
Token lifecycle logic.

```go
func (h *Handler) refreshAccessToken(ctx context.Context, host, refreshToken string) (
    accessToken string,
    expiresAt time.Time,
    rotatedRefreshToken string,
    err error,
)
```

- PAT mode returns the PAT directly (no exchange).
- Refresh-token mode:
  - POST `https://{host}/oidc/v1/token` with `grant_type=refresh_token`, `client_id=databricks-cli`.
  - Caches access token with expiry and single-flight refresh behavior.
  - If token endpoint returns a new refresh token, persist it to secrets store.

### 3. `server/internal/databricks/readonly.go` (new)
Databricks read-only SQL guard with same policy shape as BigQuery (`server/internal/bigquery/readonly.go`).

### 4. `services/databricksApi.ts` (new)
Frontend API client for Databricks settings/test calls.

```typescript
export type DatabricksStatus = {
  authMode: 'refresh_token' | 'pat' | 'none';
  hostConfigured: boolean;
  warehouseConfigured: boolean;
  host: string | null;
  warehouseId: string | null;
  hostValid: boolean;
  hostValidationMessage: string | null;
};

export type DatabricksTestResult = {
  ok: boolean;
  warehouseName: string | null;
  warehouseState: string | null;
  message: string | null;
};
```

### 5. `components/settings/DatabricksSection.tsx` (new)
Databricks settings section rendered inside the existing Cloud Credentials UI.

**UI details:**
- Host placeholder uses GCP format example: `https://1234567890123456.7.gcp.databricks.com`
- Warehouse ID input
- Auth mode radio: `Refresh Token` (recommended) / `PAT`
- SecretField writes using the explicit Databricks contract above
- Status + Test Connection actions
- Inline validation message when host is not a GCP Databricks domain

### 6. `server/internal/chat/tools/databricks_tools.go` (new)
Five server-side tools mirroring BigQuery tool wiring:
- `run_databricks_query`
- `list_databricks_catalogs`
- `list_databricks_schemas`
- `list_databricks_tables`
- `describe_databricks_table`

`run_databricks_query` uses a **preflight-first** flow (`/api/databricks/preflight` then `/api/databricks/query`) to preserve current read-only safety expectations.

---

## Files to Modify

### 7. `server/internal/secrets/store.go`
Add Databricks secret storage and status fields.

```go
type DatabricksSecrets struct {
  Mode         string `json:"mode,omitempty"` // "refresh_token" | "pat"
  RefreshToken string `json:"refresh_token,omitempty"`
  PAT          string `json:"pat,omitempty"`
}
```

Add to:
- `Plaintext`
- `ConfiguredStatus`:
  - `Databricks`
  - `DatabricksRefreshToken`
  - `DatabricksPAT`

Methods:
- `SetDatabricksRefreshToken(refreshToken string) error` (sets mode, clears PAT)
- `SetDatabricksPAT(pat string) error` (sets mode, clears refresh token)
- `ClearDatabricks(authType string) error` (`refresh_token`, `pat`, `all`)
- `GetDatabricks() (DatabricksSecrets, error)`

Rotation support:
- When refresh exchange returns `rotatedRefreshToken`, persist via `SetDatabricksRefreshToken` (or a dedicated internal helper).

### 8. `server/internal/server/handler.go`

- Extend secrets-set request body with:
```go
RefreshToken string `json:"refreshToken,omitempty"`
```

- Add `providerId=databricks` handling in `/api/secrets/set`:
  - Require `authType`.
  - Route `refresh_token` to `SetDatabricksRefreshToken(body.RefreshToken)`.
  - Route `pat` to `SetDatabricksPAT(body.APIKey)`.
  - Return 400 on invalid/missing fields.

- Add `providerId=databricks` handling in `/api/secrets/clear`:
  - Route by `authType` (`refresh_token`, `pat`, `all`).

- `HandlerOpts`: add `Databricks *databricks.Handler`.
- Mount Databricks handler:
```go
if opts.Databricks != nil {
  api.Handle("/api/databricks/", activationGate(opts.Activation, opts.Databricks))
}
```

### 9. `server/internal/chat/tools/types.go`
Add `Databricks *databricks.Handler` to `ExecContext`.

### 10. `server/internal/chat/tools/registry.go`
Register the five Databricks tools.

### 11. `server/internal/chat/optionc/manager.go`
- Add Databricks handler field on manager.
- Pass Databricks handler into `ExecContext` in `execTool`.
- Extend suppress/artifact mappings for `run_databricks_query`.

### 12. `server/cmd/ai-analyst/main.go`
- Initialize Databricks handler and wire it into:
  - chat manager constructor
  - `server.HandlerOpts`

### 13. `types.ts`
Add settings fields:

```typescript
databricksHost?: string;
databricksWarehouseId?: string;
```

### 14. `server/internal/config/settings.go`
Add server-side settings fields:

```go
DatabricksHost        *string `json:"databricksHost,omitempty"`
DatabricksWarehouseID *string `json:"databricksWarehouseId,omitempty"`
```

### 15. `services/secretsApi.ts`
Map Databricks configured flags to present keys:
- `configured.databricks` -> `provider:databricks`
- `configured.databricksRefreshToken` -> `databricks_refresh_token`
- `configured.databricksPat` -> `databricks_pat`

### 16. `components/settings/CloudCredentialsTab.tsx`
Render `DatabricksSection` in the existing tab (no new top-level Settings tab).

---

## GCP Host Enforcement

The requirement is GCP-hosted Databricks. We enforce:

- Host must resolve to `https://<workspace>.gcp.databricks.com` (including numbered subdomains like `.7.gcp.databricks.com`).
- Reject non-GCP domains (for example, `*.cloud.databricks.com`) in both:
  - server-side Databricks handler
  - UI validation feedback
- Keep canonical host normalization centralized in the Databricks package and reuse for all routes.

---

## Implementation Sequence

### Phase 1: Backend contracts + safety
1. Add Databricks secrets model + status + clear/set/get methods in `store.go`.
2. Implement explicit Databricks set/clear payload handling in `server/handler.go`.
3. Build Databricks package (`auth.go`, `readonly.go`, `handler.go`) with:
   - GCP host validation
   - read-only preflight + query guard
   - token exchange + rotation persistence
4. Mount Databricks routes and wire through `main.go` + manager.

### Phase 2: Tooling integration
5. Add Databricks to tool `ExecContext`.
6. Implement and register Databricks tools.
7. Ensure `run_databricks_query` uses preflight-first flow and stable error envelope.

### Phase 3: Frontend integration
8. Add Databricks host/warehouse settings types (TS + Go config).
9. Add `databricksApi.ts` and secrets-status mapping updates.
10. Create `DatabricksSection.tsx` and render it inside `CloudCredentialsTab.tsx`.

---

## Verification

1. **Secret contract test**: verify `/api/secrets/set` accepts only the documented Databricks payload shapes and rejects invalid combinations.
2. **Read-only safety test**:
   - `preflight` rejects `INSERT/UPDATE/DELETE/...`
   - `query` independently rejects non-read-only SQL
   - tool response marks preflight failure as rewrite-required (same behavioral model as BigQuery tool).
3. **GCP host validation test**:
   - Accept: `https://1234567890123456.7.gcp.databricks.com`
   - Reject: `https://dbc-xxx.cloud.databricks.com`
4. **Refresh-token flow test**:
   - valid refresh token exchanges to access token
   - if token endpoint returns a new `refresh_token`, it is persisted and used on subsequent refresh.
5. **PAT fallback test**: PAT mode works for status/test/query paths without refresh exchange.
6. **UI flow test**: Settings > Cloud Credentials > Databricks, set host + warehouse + auth secret, run Test Connection, verify success state.
7. **Tool execution test**: ask for catalogs/tables via chat and confirm Databricks tools execute and return expected data.
