package secrets

import "testing"

func TestSnowflakeSecretsTransitions(t *testing.T) {
	store := newUnlockedSecretsStore(t)

	_, _, cfg := store.Status()
	if cfg.Snowflake {
		t.Fatalf("Snowflake = true before set, want false")
	}
	if cfg.SnowflakeOAuthToken {
		t.Fatalf("SnowflakeOAuthToken = true before set, want false")
	}
	if cfg.SnowflakeKeyPair {
		t.Fatalf("SnowflakeKeyPair = true before set, want false")
	}

	if err := store.SetSnowflakeOAuthToken("oauth-token-123"); err != nil {
		t.Fatalf("SetSnowflakeOAuthToken: %v", err)
	}
	sec, err := store.GetSnowflake()
	if err != nil {
		t.Fatalf("GetSnowflake after oauth token set: %v", err)
	}
	if sec.Mode != "oauth_token" {
		t.Fatalf("Snowflake mode = %q, want oauth_token", sec.Mode)
	}
	if sec.OAuthToken != "oauth-token-123" {
		t.Fatalf("Snowflake OAuthToken = %q, want oauth-token-123", sec.OAuthToken)
	}
	if sec.PrivateKeyPEM != "" {
		t.Fatalf("Snowflake PrivateKeyPEM = %q, want empty", sec.PrivateKeyPEM)
	}

	_, _, cfg = store.Status()
	if !cfg.Snowflake || !cfg.SnowflakeOAuthToken || cfg.SnowflakeKeyPair {
		t.Fatalf("unexpected snowflake configured flags after oauth token set: %#v", cfg)
	}

	keyPEM := "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC...\n-----END PRIVATE KEY-----"
	if err := store.SetSnowflakeKeyPair(keyPEM, "key-passphrase"); err != nil {
		t.Fatalf("SetSnowflakeKeyPair: %v", err)
	}
	sec, err = store.GetSnowflake()
	if err != nil {
		t.Fatalf("GetSnowflake after keypair set: %v", err)
	}
	if sec.Mode != "keypair_jwt" {
		t.Fatalf("Snowflake mode = %q, want keypair_jwt", sec.Mode)
	}
	if sec.PrivateKeyPEM != keyPEM {
		t.Fatalf("Snowflake PrivateKeyPEM mismatch")
	}
	if sec.PrivateKeyPassphrase != "key-passphrase" {
		t.Fatalf("Snowflake PrivateKeyPassphrase = %q, want key-passphrase", sec.PrivateKeyPassphrase)
	}
	if sec.OAuthToken != "" {
		t.Fatalf("Snowflake OAuthToken = %q, want empty", sec.OAuthToken)
	}

	_, _, cfg = store.Status()
	if !cfg.Snowflake || cfg.SnowflakeOAuthToken || !cfg.SnowflakeKeyPair {
		t.Fatalf("unexpected snowflake configured flags after keypair set: %#v", cfg)
	}

	if err := store.ClearSnowflake(); err != nil {
		t.Fatalf("ClearSnowflake: %v", err)
	}
	sec, err = store.GetSnowflake()
	if err != nil {
		t.Fatalf("GetSnowflake after clear: %v", err)
	}
	if sec.Mode != "oauth_token" {
		t.Fatalf("Snowflake mode after clear = %q, want oauth_token default", sec.Mode)
	}
	if sec.OAuthToken != "" || sec.PrivateKeyPEM != "" || sec.PrivateKeyPassphrase != "" {
		t.Fatalf("Snowflake secrets should be cleared, got %#v", sec)
	}
	_, _, cfg = store.Status()
	if cfg.Snowflake || cfg.SnowflakeOAuthToken || cfg.SnowflakeKeyPair {
		t.Fatalf("snowflake configured flags should be false after clear, got %#v", cfg)
	}
}

func TestSnowflakeKeyPairPEMRoundTripEncrypted(t *testing.T) {
	store := newUnlockedSecretsStore(t)

	privateKeyPEM := "-----BEGIN PRIVATE KEY-----\nMIIJQwIBADANBgkqhkiG9w0BAQEFAASCCT0wggk5AgEAAoICAQD...\n-----END PRIVATE KEY-----"
	passphrase := "super-secret-passphrase"

	if err := store.SetSnowflakeKeyPair(privateKeyPEM, passphrase); err != nil {
		t.Fatalf("SetSnowflakeKeyPair: %v", err)
	}

	before, err := store.GetSnowflake()
	if err != nil {
		t.Fatalf("GetSnowflake before lock: %v", err)
	}
	if before.Mode != "keypair_jwt" {
		t.Fatalf("mode before lock = %q, want keypair_jwt", before.Mode)
	}
	if before.PrivateKeyPEM != privateKeyPEM {
		t.Fatalf("private key PEM mismatch before lock")
	}
	if before.PrivateKeyPassphrase != passphrase {
		t.Fatalf("passphrase mismatch before lock")
	}

	store.Lock()
	if err := store.Unlock("passphrase"); err != nil {
		t.Fatalf("Unlock: %v", err)
	}

	after, err := store.GetSnowflake()
	if err != nil {
		t.Fatalf("GetSnowflake after unlock: %v", err)
	}
	if after.Mode != "keypair_jwt" {
		t.Fatalf("mode after unlock = %q, want keypair_jwt", after.Mode)
	}
	if after.PrivateKeyPEM != privateKeyPEM {
		t.Fatalf("private key PEM mismatch after unlock")
	}
	if after.PrivateKeyPassphrase != passphrase {
		t.Fatalf("passphrase mismatch after unlock")
	}
}
