package snowflake

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"

	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/secrets"
)

func TestNormalizeSnowflakeAccountIdentifierMatrix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		raw         string
		wantAccount string
		wantHost    string
		wantErr     bool
	}{
		{
			name:        "org-account",
			raw:         "org-account",
			wantAccount: "org-account",
			wantHost:    "org-account.snowflakecomputing.com",
		},
		{
			name:        "locator region cloud suffix",
			raw:         "xy12345.us-east-1.aws",
			wantAccount: "xy12345.us-east-1.aws",
			wantHost:    "xy12345.us-east-1.aws.snowflakecomputing.com",
		},
		{
			name:        "full host",
			raw:         "xy12345.us-east-1.aws.snowflakecomputing.com",
			wantAccount: "xy12345.us-east-1.aws",
			wantHost:    "xy12345.us-east-1.aws.snowflakecomputing.com",
		},
		{
			name:        "privatelink host",
			raw:         "xy12345.us-east-1.privatelink.snowflakecomputing.com",
			wantAccount: "xy12345.us-east-1.privatelink",
			wantHost:    "xy12345.us-east-1.privatelink.snowflakecomputing.com",
		},
		{
			name:    "http scheme rejected",
			raw:     "http://xy12345.snowflakecomputing.com",
			wantErr: true,
		},
		{
			name:    "path rejected in URL",
			raw:     "https://xy12345.snowflakecomputing.com/path",
			wantErr: true,
		},
		{
			name:    "path rejected in host-like input",
			raw:     "xy12345.snowflakecomputing.com/path",
			wantErr: true,
		},
		{
			name:    "malformed host rejected",
			raw:     "snowflakecomputing.com",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			account, host, err := normalizeSnowflakeAccountIdentifier(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("normalizeSnowflakeAccountIdentifier(%q) error=nil, want error", tc.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeSnowflakeAccountIdentifier(%q): %v", tc.raw, err)
			}
			if account != tc.wantAccount {
				t.Fatalf("account=%q, want %q", account, tc.wantAccount)
			}
			if host != tc.wantHost {
				t.Fatalf("host=%q, want %q", host, tc.wantHost)
			}
		})
	}
}

func TestAuthConfigOAuthTokenFromCompatibleSecrets(t *testing.T) {
	t.Parallel()

	h, _, sec := newAuthTestHandler(t)
	if err := sec.SetSnowflakeOAuthToken("tok-123"); err != nil {
		t.Fatalf("SetSnowflakeOAuthToken: %v", err)
	}

	cfg, err := h.authConfig(authRequestOverrides{AccountIdentifier: "org-account"})
	if err != nil {
		t.Fatalf("authConfig: %v", err)
	}
	if cfg.Mode != snowflakeAuthModeOAuthToken {
		t.Fatalf("Mode=%q, want %q", cfg.Mode, snowflakeAuthModeOAuthToken)
	}
	if got := strings.TrimSpace(cfg.Authorization); got != "Bearer tok-123" {
		t.Fatalf("Authorization=%q, want Bearer tok-123", got)
	}
	if got := strings.TrimSpace(cfg.BaseURL); got != "https://org-account.snowflakecomputing.com" {
		t.Fatalf("BaseURL=%q, want https://org-account.snowflakecomputing.com", got)
	}
}

func TestAuthConfigKeypairJWTClaimsAndCache(t *testing.T) {
	t.Parallel()

	h, _, sec := newAuthTestHandler(t)
	pemText, key := testRSAPrivateKeyPEM(t)
	if err := sec.SetSnowflakeKeyPair(pemText, ""); err != nil {
		t.Fatalf("SetSnowflakeKeyPair: %v", err)
	}

	cfg1, err := h.authConfig(authRequestOverrides{AccountIdentifier: "org-account", User: "svc_user"})
	if err != nil {
		t.Fatalf("first authConfig: %v", err)
	}
	cfg2, err := h.authConfig(authRequestOverrides{AccountIdentifier: "org-account", User: "svc_user"})
	if err != nil {
		t.Fatalf("second authConfig: %v", err)
	}

	if cfg1.Mode != snowflakeAuthModeKeypairJWT {
		t.Fatalf("Mode=%q, want %q", cfg1.Mode, snowflakeAuthModeKeypairJWT)
	}
	if !strings.HasPrefix(cfg1.Authorization, "Bearer ") {
		t.Fatalf("Authorization does not have Bearer prefix: %q", cfg1.Authorization)
	}
	if cfg1.Authorization != cfg2.Authorization {
		t.Fatalf("expected cached JWT to be reused; got different tokens")
	}

	token := strings.TrimSpace(strings.TrimPrefix(cfg1.Authorization, "Bearer "))
	claims := decodeJWTClaims(t, token)

	sub, _ := claims["sub"].(string)
	if sub != "ORG-ACCOUNT.SVC_USER" {
		t.Fatalf("sub=%q, want ORG-ACCOUNT.SVC_USER", sub)
	}
	iss, _ := claims["iss"].(string)
	fingerprint, err := snowflakePublicKeyFingerprint(&key.PublicKey)
	if err != nil {
		t.Fatalf("snowflakePublicKeyFingerprint: %v", err)
	}
	wantIss := "ORG-ACCOUNT.SVC_USER." + fingerprint
	if iss != wantIss {
		t.Fatalf("iss=%q, want %q", iss, wantIss)
	}
	iat, okIat := claims["iat"].(float64)
	exp, okExp := claims["exp"].(float64)
	if !okIat || !okExp || exp <= iat {
		t.Fatalf("invalid iat/exp claims: iat=%v exp=%v", claims["iat"], claims["exp"])
	}
}

func TestAuthConfigKeypairEncryptedPEM(t *testing.T) {
	t.Parallel()

	h, _, sec := newAuthTestHandler(t)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	block, err := x509.EncryptPEMBlock(rand.Reader, "RSA PRIVATE KEY", der, []byte("p4ss"), x509.PEMCipherAES256)
	if err != nil {
		t.Fatalf("EncryptPEMBlock: %v", err)
	}
	encryptedPEM := string(pem.EncodeToMemory(block))

	if err := sec.SetSnowflakeKeyPair(encryptedPEM, "p4ss"); err != nil {
		t.Fatalf("SetSnowflakeKeyPair: %v", err)
	}

	cfg, err := h.authConfig(authRequestOverrides{AccountIdentifier: "org-account", User: "svc_user"})
	if err != nil {
		t.Fatalf("authConfig: %v", err)
	}
	if !strings.HasPrefix(cfg.Authorization, "Bearer ") {
		t.Fatalf("Authorization does not have Bearer prefix: %q", cfg.Authorization)
	}
}

func TestAuthConfigRejectsInvalidMode(t *testing.T) {
	t.Parallel()

	_, err := validateSnowflakeSecrets(snowflakeSecretsV1{
		Mode:       "something_else",
		OAuthToken: "tok-123",
	})
	if err == nil {
		t.Fatalf("expected error for invalid auth mode")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "invalid") {
		t.Fatalf("error=%v, want invalid mode message", err)
	}
}

func testRSAPrivateKeyPEM(t *testing.T) (string, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	pemText := string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}))
	return pemText, key
}

func decodeJWTClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("invalid JWT token format")
	}
	claimsPayload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("DecodeString(claims): %v", err)
	}
	out := map[string]any{}
	if err := json.Unmarshal(claimsPayload, &out); err != nil {
		t.Fatalf("Unmarshal claims: %v", err)
	}
	return out
}

func newAuthTestHandler(t *testing.T) (*Handler, *config.Store, *secrets.Store) {
	t.Helper()
	dir := t.TempDir()
	settings, err := config.NewStore(dir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	sec, err := secrets.NewStore(dir)
	if err != nil {
		t.Fatalf("secrets.NewStore: %v", err)
	}
	if err := sec.Init("passphrase"); err != nil {
		t.Fatalf("secrets.Init: %v", err)
	}
	if err := sec.Unlock("passphrase"); err != nil {
		t.Fatalf("secrets.Unlock: %v", err)
	}
	return NewHandler(settings, sec), settings, sec
}
