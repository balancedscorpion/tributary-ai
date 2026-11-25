# Activation Key Procurement Guide (Offline, Device-Bound)

This product uses **Activation** (not online accounts) to unlock server-side features. Activation is:

- **Offline**: no calls to an activation server.
- **Device-bound**: the activation key only works for the Device ID it was issued to.
- **Time-limited**: activation keys can expire; default issuance is **180 days** (configurable).

This guide explains how to **procure** (request + issue) activation keys.

## Terms

- **Device ID**: a stable identifier used to bind activation to a single device/runtime. It is generated from a local seed stored in the app config directory (persisted via a Docker volume when running in Docker).
- **Activation key**: a signed string you paste into the app to activate it.
- **Issuer**: the admin/vendor who holds the private signing key and generates activation keys.

## End User: How To Request Activation

1. Start the app (Docker).
2. Open `Settings` (gear icon).
3. Go to `Activation`.
4. Copy the `Device ID`.
5. Send the `Device ID` to your admin/vendor (email/chat) and ask for an activation key.
6. Paste the activation key into `Activation Key` and click `Activate`.

### Activation Request Template (copy/paste)

Subject: Activation request

Body:

- Product: AI Analyst (Local)
- Device ID: `<PASTE_DEVICE_ID_HERE>`
- Requested validity: `180 days` (default) or `<N> days`

### If Device ID Is Blank (Docker)

The app must be able to persist its config directory. Ensure you are mounting a persistent config volume:

- Mount: `ai_analyst_config:/home/nonroot/.config/aianalyst`

## Issuer/Admin: How To Issue Activation Keys

### Step 1: Create and Store a Signing Key (one-time)

Generate an Ed25519 private key and store it securely. Example:

```bash
openssl genpkey -algorithm ed25519 -out activation_private.pem
```

Keep `activation_private.pem` offline and restricted. Anyone with this key can generate valid activations.

### Step 2: Configure the App’s Public Key (one-time per release line)

The app verifies activation keys using an embedded Ed25519 public key.

1. Derive the public key snippet and paste it into the server:

```bash
cd server
go run ./cmd/activationgen --private-key ../activation_private.pem --fingerprint dummy --print-public-key-go > /dev/null
```

2. Replace `DefaultPublicKey` in `server/internal/activation/publickey.go` with that snippet.
3. Rebuild the Docker image / release artifacts.

### Step 3: Generate an Activation Key (per device)

You need the user’s `Device ID` (copied from Settings -> Activation).

Default expiry is 180 days:

```bash
cd server
go run ./cmd/activationgen \
  --private-key ../activation_private.pem \
  --fingerprint "<DEVICE_ID_FROM_USER>" \
  --issued-to "Customer Name"
```

Custom expiry (example: 365 days):

```bash
cd server
go run ./cmd/activationgen \
  --private-key ../activation_private.pem \
  --expires-in-days 365 \
  --fingerprint "<DEVICE_ID_FROM_USER>" \
  --issued-to "Customer Name"
```

Send the printed activation key string back to the user to paste into the UI.

## Troubleshooting

- `DEVICE_ID_MISMATCH`: the activation key was generated for a different machine. Confirm the user copied the correct Device ID.
- `EXPIRED`: generate a new activation with a later expiry.
- `PUBLIC_KEY_NOT_CONFIGURED`: the shipped app does not contain the matching public key.
- Clock issues: if the machine clock is very wrong, expiry checks can appear incorrect. Fix system time and retry.
