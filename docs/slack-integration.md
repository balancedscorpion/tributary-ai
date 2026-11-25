# Slack Integration

Date: 2026-06-06

## Summary

Slack integration is OAuth-only. The app must never ask a user to paste a Slack bot token, user token, personal token, incoming webhook URL, or other manual Slack credential.

The implemented model is local OAuth with PKCE first. It keeps the local setup
close to the Databricks pattern: settings hold public connection metadata, while
OAuth-issued credential material is created by the login flow and stored
encrypted locally. Optional encrypted storage exists for the Slack app's OAuth
Client Secret only as a fallback for confidential Slack apps that cannot use
PKCE.

- The user starts Slack connection from the app settings UI.
- The backend creates a short-lived OAuth state and PKCE verifier.
- The frontend opens the Slack authorization URL returned by the backend.
- Slack redirects back to the local `/api/integrations/slack/oauth/callback` route.
- The backend validates state, exchanges the temporary code with Slack, and stores OAuth-issued credentials in the encrypted local secrets store.
- The frontend receives only connection status and workspace metadata.

This follows the repository preference for local logic and existing configuration over environment variables. The Slack OAuth client ID is public app metadata in settings. The preferred Slack setup enables PKCE and does not require any Slack app secret in the local app. The Slack OAuth Client Secret, when used as an advanced fallback, is stored in encrypted local secrets. Slack access and refresh credentials are internal OAuth results stored in encrypted secrets, not user-provided tokens.

Slack write tools are optional and approval-gated. When enabled locally, the model can prepare Slack message posts and Slack file uploads, but the user must approve the exact destination and content before the backend sends anything to Slack.

## Redirect URL

The callback URL must exactly match the URL Slack sees during the OAuth flow. For loopback development URLs, the implementation normalizes `127.0.0.1` and `::1` origins to `localhost` before sending the redirect URI to Slack. Slack's PKCE docs specifically identify `localhost` redirects as desktop redirects for PKCE-enabled apps.

Common local callback URLs:

```text
http://localhost:5173/api/integrations/slack/oauth/callback
```

Use the URL returned by `/api/integrations/slack/oauth/start` as the source of truth and add that exact URL to the Slack app's OAuth redirect URLs. `localhost` and `127.0.0.1` are different redirect URLs to Slack, and different ports are also different redirects. For PKCE mode, prefer the returned `localhost` URL.

If the local app shows `bad_client_secret` even though the generated Slack URL
contains `code_challenge`, `code_challenge_method=S256`, and a `localhost`
redirect, Slack is not treating that Client ID as a PKCE-enabled public app.
Check the PKCE setting under OAuth & Permissions for the exact Slack app whose
Client ID is configured locally. For directory apps, Slack documents that PKCE
is configured in Published App Settings.

When an authorization request includes `redirect_uri`, the code exchange request must use the same value. The backend preserves this per pending OAuth state.

## Slack App Setup

1. Create or select a Slack app in the Slack developer console.
2. In OAuth settings, enable PKCE for the Slack app.
3. In OAuth settings, add the callback URL returned by this application.
4. Add the requested user token scopes listed below. Read tools only need the read scopes; approval-gated post/upload tools additionally need `chat:write` and `files:write`.
5. Copy the Slack app client ID into the local Slack settings field.
6. If PKCE cannot be enabled and Slack returns `bad_client_secret`, copy the app's Client Secret from
   Basic Information -> App Credentials into the Slack OAuth Client Secret
   field under Advanced OAuth app credential and save it. This is an OAuth app
   credential, not a Slack access token.
7. Install the app through the application's Connect flow.

Do not copy `xoxb-*`, `xoxp-*`, `xoxe-*`, webhook URLs, or any other Slack access token into this app. There is no manual Slack token field.

The current implementation uses PKCE for the local OAuth flow. Slack apps with
PKCE enabled can connect with just the Client ID for localhost user-token
authorization. Slack apps that require a Client Secret use the encrypted local
Client Secret field; the backend sends it to Slack's OAuth exchange endpoint
with HTTP Basic authentication. The Client Secret must not be stored in frontend
localStorage or environment variables.

Slack treats localhost PKCE callbacks as desktop redirects, and desktop
redirects cannot request bot scopes. For local localhost testing, the Connect
flow requests the user profile only. Bot-authored writes require an existing bot
profile from a Slack OAuth web redirect that is not localhost.

## OAuth Flow

1. `POST /api/integrations/slack/oauth/start`
   - Backend reads the configured Slack OAuth client ID.
   - Backend generates a high-entropy state value.
   - Backend stores pending state locally with a short expiration.
   - Backend generates a PKCE verifier and S256 code challenge.
   - Backend returns the Slack authorization URL and the exact redirect URI.
2. The frontend opens the returned Slack authorization URL.
3. Slack redirects to `GET /api/integrations/slack/oauth/callback?code=...&state=...`.
4. Backend validates state before making any Slack API call.
5. Backend exchanges the code with Slack's `oauth.v2.access` API using:
   - public `client_id`
   - `code`
   - matching `redirect_uri`
   - PKCE `code_verifier`
   - no `client_secret` in the first PKCE exchange attempt.
   - HTTP Basic authentication with the app Client ID and Client Secret only as
     an advanced fallback when a Client Secret is saved locally and Slack rejects
     the public-client exchange.
6. Backend stores OAuth-issued credential material and workspace metadata in encrypted local storage.
7. Frontend polls `GET /api/integrations/slack/oauth/result?state=...` and `GET /api/integrations/slack/status` for metadata only.

The OAuth callback renders a minimal local success or failure page. It must not expose tokens in the page body, logs, redirects, query strings after callback handling, or browser storage.

## Local Encrypted Secret Storage

Slack secrets are stored in `server/internal/secrets` alongside the existing encrypted local secrets model.

Stored fields:

- `client_secret`, the Slack OAuth app Client Secret, when required by the
  Slack app. This is not a Slack access token.
- `access_token`, the OAuth-issued Slack user token used internally by the backend for read tools and optional "send as me" writes.
- `refresh_token`, when Slack token rotation returns one for the user profile.
- `expires_at`, when Slack token rotation returns a user-token expiry.
- `token_type`
- `scope`
- `bot_access_token`, the OAuth-issued Slack bot token used internally by the backend for optional "send as bot" writes.
- `bot_refresh_token`, when Slack token rotation returns one for the bot profile.
- `bot_expires_at`, when Slack token rotation returns a bot-token expiry.
- `bot_token_type`
- `bot_scope`
- `bot_user_id`
- `bot_id`
- `team_id`
- `team_name`
- `enterprise_id`
- `authed_user_id`
- `installed_at`
- `last_refreshed`

Configured status exposes display metadata only:

- `slack`
- `slackClientSecret`
- `slackTeamId`
- `slackTeamName`
- `slackScope`
- `slackBotScope`
- `slackAuthedUserId`
- `slackBotUserId`

Token values are write-only from the frontend perspective. Status, check, and OAuth result endpoints expose booleans, timestamps, messages, and Slack workspace metadata, never token values.

Disconnect clears Slack OAuth material from the encrypted store and preserves
the saved Client Secret so reconnecting does not require re-entering the app
credential. The Client Secret can be cleared separately from the Slack settings
UI.

## Slack Identities

OAuth can initialize two local profiles when the Slack app and redirect URI support both bot scopes and user scopes:

- User profile: read tools and optional write tools that should appear from the connected Slack user.
- Bot profile: optional write tools that should appear from the Slack app's bot identity.

Settings expose a default send identity for Slack write tools. The tools also accept `send_as` with `user` or `bot`; when omitted, the settings default is used. Approvals include the selected identity so the user can see whether a message or file will be sent as them or as the bot before approving.

With the default local `http://localhost:5173/...` PKCE redirect, only the user
profile is initialized because Slack rejects bot scopes for that redirect type.
The status endpoint reports `bot_oauth_available=false` and a blocker message so
the UI can disable bot initialization instead of generating an invalid Slack
authorization URL.

## Scopes

The current implementation always requests user token scopes for reads and optional user-authored writes. It requests bot token scopes for bot-authored writes only when the redirect URI is a Slack web redirect that allows bot scopes.

Default requested user scopes:

- `team:read` - verify connected workspace identity.
- `users:read` - resolve Slack user IDs and names.
- `channels:read` - list and inspect public channel metadata.
- `channels:history` - read public channel messages visible to the authorized user.
- `groups:read` - list and inspect private channel metadata visible to the authorized user.
- `groups:history` - read private channel messages visible to the authorized user.
- `im:read` - inspect direct-message conversations visible to the authorized user.
- `im:history` - read direct-message history visible to the authorized user.
- `mpim:read` - inspect multi-party direct messages visible to the authorized user.
- `mpim:history` - read multi-party direct-message history visible to the authorized user.
- `files:read` - read file metadata visible to the authorized user.
- `search:read` - search messages visible to the authorized user.
- `chat:write` - post approval-gated Slack messages.
- `files:write` - upload and share approval-gated Slack files.

Default requested bot scopes:

- `chat:write` - post approval-gated Slack messages as the Slack app bot.
- `files:write` - upload and share approval-gated Slack files as the Slack app bot.

Avoid administrative scopes, channel-management scopes, and user-write scopes. The implemented Slack write surface is deliberately limited to approval-gated message posting and file upload.

## No Manual Token Path

Unsupported paths:

- Manual Slack access token input.
- Incoming webhook URL input as an auth substitute.
- Environment variables for Slack access tokens.
- Frontend localStorage for Slack tokens.
- Provider-compatible secret header profiles as a Slack auth workaround.
- Migration paths that preserve old manually entered Slack tokens.

If a future implementation discovers legacy Slack token fields, migration should delete them and require a fresh OAuth connection.

## Runtime Boundaries

Slack content is untrusted. Slack messages, files, channel topics, and user profile text must be treated as data, not instructions.

Slack tools are gated by both connection status and local Slack settings:

Read tools:

- `slack_status`
- `slack_check_connection`
- `slack_list_conversations`
- `slack_get_conversation`
- `slack_get_conversation_members`
- `slack_get_messages`
- `slack_get_thread_replies`
- `slack_get_user`
- `slack_search_messages`

Write tools:

- `slack_post_message`
- `slack_upload_file`

Read tools use the backend-owned user OAuth credential and return bounded data pages with Slack cursors when pagination is available. Write tools queue approvals and do not mutate Slack until the user approves the exact target, identity, and content in the chat UI.

`slack_upload_file` uses Slack's external upload flow: `files.getUploadURLExternal`, raw byte upload to Slack's upload URL, and `files.completeUploadExternal`. It does not use the deprecated `files.upload` endpoint. The tool accepts exactly one canonical local source: a thread attachment `artifact_id`, a project-relative `path`, an app-managed downloaded file `local_path`, inline `contentText`, or inline `contentBase64`. `local_path` is restricted to app-managed download directories such as `atlassian/downloads/` and cannot reference arbitrary files or secrets elsewhere on disk. The approval preview includes the file name, MIME type, byte size, SHA-256, source type, source reference, and text preview when available. Approval execution returns Slack file ID, Slack file metadata, permalink/private download URLs when Slack returns them, the original local source type/reference, and SHA-256.

Optional allowlists in settings can restrict access to specific Slack team IDs and channel IDs. Channel-specific methods enforce the channel allowlist, and conversation listing is filtered when a channel allowlist exists.

## Limitations

- Slack APIs only return content allowed by the authorized user, requested scopes, and workspace policy.
- Private channels and direct messages require the corresponding scopes and user visibility.
- Some workspaces require administrator approval before app installation or scope changes.
- Slack Connect and Enterprise Grid metadata can differ by workspace and enterprise install context.
- GovSlack requires Slack's GovSlack domains and should be treated as a separate compatibility target.
- Rate limits should be handled with bounded retries and clear user-facing errors.
- OAuth scopes are additive across repeated installs. To reduce scopes, the user may need to reinstall or revoke the Slack app from the Slack workspace.
- This integration does not define event ingestion, Socket Mode, background indexing, or always-on Slack synchronization. The initial implementation uses on-demand Web API calls from local tools.

## References

- [Slack: Installing with OAuth](https://docs.slack.dev/authentication/installing-with-oauth/)
- [Slack: OAuth with token rotation](https://docs.slack.dev/authentication/using-token-rotation/)
- [Slack: oauth.v2.access](https://docs.slack.dev/reference/methods/oauth.v2.access/)
- [Slack: Conversations API](https://docs.slack.dev/reference/methods/conversations.list/)
- [Slack: search.messages](https://docs.slack.dev/reference/methods/search.messages/)
- [Slack: channels:read scope](https://docs.slack.dev/reference/scopes/channels.read/)
- [Slack: channels:history scope](https://docs.slack.dev/reference/scopes/channels.history/)
- [Slack: groups:read scope](https://docs.slack.dev/reference/scopes/groups.read/)
- [Slack: groups:history scope](https://docs.slack.dev/reference/scopes/groups.history/)
- [Slack: im:read scope](https://docs.slack.dev/reference/scopes/im.read/)
- [Slack: im:history scope](https://docs.slack.dev/reference/scopes/im.history/)
- [Slack: mpim:read scope](https://docs.slack.dev/reference/scopes/mpim.read/)
- [Slack: mpim:history scope](https://docs.slack.dev/reference/scopes/mpim.history/)
- [Slack: users:read scope](https://docs.slack.dev/reference/scopes/users.read/)
- [Slack: team:read scope](https://docs.slack.dev/reference/scopes/team.read/)
- [Slack: files:read scope](https://docs.slack.dev/reference/scopes/files.read/)
- [Slack: search:read scope](https://docs.slack.dev/reference/scopes/search.read/)
