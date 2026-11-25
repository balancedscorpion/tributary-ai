package snowflake

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"time"

	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/secrets"
)

const (
	snowflakeAuthModeOAuthToken = "oauth_token"
	snowflakeAuthModeKeypairJWT = "keypair_jwt"

	snowflakeJWTLifetime    = 55 * time.Minute
	snowflakeJWTRefreshSkew = 2 * time.Minute

	snowflakeCompatibleProfileID = "snowflake"
)

var snowflakeIdentifierPartRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

type authRequestOverrides struct {
	AccountIdentifier string
	Warehouse         string
	Database          string
	Schema            string
	Role              string
	User              string
}

type snowflakeSettingsSnapshot struct {
	AccountIdentifier string
	Warehouse         string
	Database          string
	Schema            string
	Role              string
	User              string
}

type snowflakeAuthConfig struct {
	Mode              string
	BaseURL           string
	Host              string
	AccountIdentifier string
	Warehouse         string
	Database          string
	Schema            string
	Role              string
	User              string
	Authorization     string
	TokenType         string
	RequestID         string
}

type snowflakeSecretsV1 struct {
	Mode                 string
	OAuthToken           string
	PrivateKeyPEM        string
	PrivateKeyPassphrase string
}

func (h *Handler) settingsSnapshot() snowflakeSettingsSnapshot {
	out := snowflakeSettingsSnapshot{}
	if h == nil || h.Settings == nil {
		return out
	}
	s := h.Settings.Get()
	out.AccountIdentifier = settingStringField(s, "SnowflakeAccountIdentifier")
	out.Warehouse = settingStringField(s, "SnowflakeWarehouse")
	out.Database = settingStringField(s, "SnowflakeDatabase")
	out.Schema = settingStringField(s, "SnowflakeSchema")
	out.Role = settingStringField(s, "SnowflakeRole")
	out.User = settingStringField(s, "SnowflakeUser")
	return out
}

func settingStringField(settings config.AppSettings, fieldNames ...string) string {
	rv := reflect.ValueOf(settings)
	for _, name := range fieldNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		f := rv.FieldByName(name)
		if !f.IsValid() {
			continue
		}
		switch f.Kind() {
		case reflect.String:
			if v := strings.TrimSpace(f.String()); v != "" {
				return v
			}
		case reflect.Pointer:
			if f.IsNil() {
				continue
			}
			if f.Elem().Kind() == reflect.String {
				if v := strings.TrimSpace(f.Elem().String()); v != "" {
					return v
				}
			}
		}
	}
	return ""
}

func (h *Handler) statusAuthMode() (string, *snowflakeSecretsV1, error) {
	if h == nil || h.Secrets == nil {
		sec := &snowflakeSecretsV1{Mode: "none"}
		return sec.Mode, sec, nil
	}
	sec, err := loadSnowflakeSecrets(h.Secrets)
	if err != nil {
		return "none", nil, err
	}
	return sec.Mode, &sec, nil
}

func (h *Handler) authConfig(overrides authRequestOverrides) (snowflakeAuthConfig, error) {
	settings := h.settingsSnapshot()
	sec, err := loadSnowflakeSecrets(h.Secrets)
	if err != nil {
		return snowflakeAuthConfig{}, httpError{status: http.StatusUnauthorized, msg: err.Error()}
	}
	if sec.Mode != snowflakeAuthModeOAuthToken && sec.Mode != snowflakeAuthModeKeypairJWT {
		return snowflakeAuthConfig{}, httpError{status: http.StatusUnauthorized, msg: "Snowflake credentials are not configured. Set oauth_token or keypair_jwt in Settings."}
	}

	accountRaw := firstNonEmpty(overrides.AccountIdentifier, settings.AccountIdentifier)
	accountIdentifier, host, err := normalizeSnowflakeAccountIdentifier(accountRaw)
	if err != nil {
		return snowflakeAuthConfig{}, err
	}

	cfg := snowflakeAuthConfig{
		Mode:              sec.Mode,
		BaseURL:           "https://" + host,
		Host:              host,
		AccountIdentifier: accountIdentifier,
		Warehouse:         firstNonEmpty(overrides.Warehouse, settings.Warehouse),
		Database:          firstNonEmpty(overrides.Database, settings.Database),
		Schema:            firstNonEmpty(overrides.Schema, settings.Schema),
		Role:              firstNonEmpty(overrides.Role, settings.Role),
		User:              firstNonEmpty(overrides.User, settings.User),
	}

	switch sec.Mode {
	case snowflakeAuthModeOAuthToken:
		cfg.Authorization = "Bearer " + strings.TrimSpace(sec.OAuthToken)
		cfg.TokenType = "OAUTH"
	case snowflakeAuthModeKeypairJWT:
		if strings.TrimSpace(cfg.User) == "" {
			return snowflakeAuthConfig{}, httpError{status: http.StatusBadRequest, msg: "Missing snowflakeUser in settings for keypair_jwt auth"}
		}
		tok, err := h.keypairJWT(accountIdentifier, cfg.User, sec.PrivateKeyPEM, sec.PrivateKeyPassphrase)
		if err != nil {
			return snowflakeAuthConfig{}, httpError{status: http.StatusUnauthorized, msg: err.Error()}
		}
		cfg.Authorization = "Bearer " + tok
		cfg.TokenType = "KEYPAIR_JWT"
	default:
		return snowflakeAuthConfig{}, httpError{status: http.StatusBadRequest, msg: "invalid Snowflake auth mode"}
	}

	return cfg, nil
}

func loadSnowflakeSecrets(store *secrets.Store) (snowflakeSecretsV1, error) {
	if store == nil {
		return snowflakeSecretsV1{}, errors.New("Snowflake credentials are unavailable: secrets store is not configured")
	}

	if typed, ok, err := loadTypedSnowflakeSecrets(store); ok {
		if err != nil {
			return snowflakeSecretsV1{}, err
		}
		return validateSnowflakeSecrets(typed)
	}

	fallback, err := loadCompatibleSnowflakeSecrets(store)
	if err != nil {
		return snowflakeSecretsV1{}, err
	}
	return validateSnowflakeSecrets(fallback)
}

func loadTypedSnowflakeSecrets(store *secrets.Store) (snowflakeSecretsV1, bool, error) {
	method := reflect.ValueOf(store).MethodByName("GetSnowflake")
	if !method.IsValid() {
		return snowflakeSecretsV1{}, false, nil
	}
	results := method.Call(nil)
	if len(results) != 2 {
		return snowflakeSecretsV1{}, true, errors.New("Snowflake secrets accessor has unexpected signature")
	}
	if errVal := results[1]; errVal.IsValid() && !errVal.IsNil() {
		err, _ := errVal.Interface().(error)
		if err != nil {
			return snowflakeSecretsV1{}, true, err
		}
		return snowflakeSecretsV1{}, true, errors.New("Snowflake secrets accessor returned non-error failure")
	}

	value := results[0]
	if !value.IsValid() {
		return snowflakeSecretsV1{}, true, nil
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return snowflakeSecretsV1{}, true, nil
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return snowflakeSecretsV1{}, true, errors.New("Snowflake secrets accessor returned unexpected payload")
	}

	sec := snowflakeSecretsV1{
		Mode:                 structFieldString(value, "Mode"),
		OAuthToken:           structFieldString(value, "OAuthToken"),
		PrivateKeyPEM:        structFieldString(value, "PrivateKeyPEM"),
		PrivateKeyPassphrase: structFieldString(value, "PrivateKeyPassphrase"),
	}
	return sec, true, nil
}

func loadCompatibleSnowflakeSecrets(store *secrets.Store) (snowflakeSecretsV1, error) {
	comp, err := store.GetCompatible(snowflakeCompatibleProfileID)
	if err != nil {
		return snowflakeSecretsV1{}, err
	}

	mode := secretHeaderLookup(comp.SecretHeaders,
		"auth_type",
		"authType",
		"mode",
	)
	oauthToken := firstNonEmpty(
		comp.APIKey,
		secretHeaderLookup(comp.SecretHeaders, "oauth_token", "oauthToken", "token"),
	)
	privateKeyPEM := firstNonEmpty(
		secretHeaderLookup(comp.SecretHeaders, "private_key_pem", "privateKeyPem", "private_key"),
	)
	privateKeyPassphrase := firstNonEmpty(
		secretHeaderLookup(comp.SecretHeaders, "private_key_passphrase", "privateKeyPassphrase", "passphrase"),
	)

	return snowflakeSecretsV1{
		Mode:                 mode,
		OAuthToken:           oauthToken,
		PrivateKeyPEM:        privateKeyPEM,
		PrivateKeyPassphrase: privateKeyPassphrase,
	}, nil
}

func validateSnowflakeSecrets(sec snowflakeSecretsV1) (snowflakeSecretsV1, error) {
	sec.Mode = strings.ToLower(strings.TrimSpace(sec.Mode))
	sec.OAuthToken = strings.TrimSpace(sec.OAuthToken)
	sec.PrivateKeyPEM = strings.TrimSpace(sec.PrivateKeyPEM)
	sec.PrivateKeyPassphrase = strings.TrimSpace(sec.PrivateKeyPassphrase)

	if sec.Mode == "" {
		switch {
		case sec.OAuthToken != "":
			sec.Mode = snowflakeAuthModeOAuthToken
		case sec.PrivateKeyPEM != "":
			sec.Mode = snowflakeAuthModeKeypairJWT
		default:
			sec.Mode = "none"
		}
	}

	switch sec.Mode {
	case "none":
		return sec, nil
	case snowflakeAuthModeOAuthToken:
		if sec.OAuthToken == "" {
			if sec.PrivateKeyPEM == "" {
				sec.Mode = "none"
				return sec, nil
			}
			return snowflakeSecretsV1{}, errors.New("Snowflake oauth_token auth is selected but oauth token is not configured")
		}
		sec.PrivateKeyPEM = ""
		sec.PrivateKeyPassphrase = ""
		return sec, nil
	case snowflakeAuthModeKeypairJWT:
		if sec.PrivateKeyPEM == "" {
			if sec.OAuthToken == "" {
				sec.Mode = "none"
				return sec, nil
			}
			return snowflakeSecretsV1{}, errors.New("Snowflake keypair_jwt auth is selected but private key PEM is not configured")
		}
		sec.OAuthToken = ""
		return sec, nil
	default:
		return snowflakeSecretsV1{}, errors.New("invalid Snowflake auth mode in secrets store")
	}
}

func secretHeaderLookup(headers map[string]string, keys ...string) string {
	if len(headers) == 0 {
		return ""
	}
	normalized := map[string]string{}
	for key, value := range headers {
		normKey := normalizeSecretKey(key)
		if normKey == "" {
			continue
		}
		normalized[normKey] = strings.TrimSpace(value)
	}
	for _, key := range keys {
		norm := normalizeSecretKey(key)
		if norm == "" {
			continue
		}
		if value := strings.TrimSpace(normalized[norm]); value != "" {
			return value
		}
	}
	return ""
}

func normalizeSecretKey(key string) string {
	key = strings.TrimSpace(strings.ToLower(key))
	if key == "" {
		return ""
	}
	b := strings.Builder{}
	b.Grow(len(key))
	for i := 0; i < len(key); i++ {
		c := key[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteByte(c)
		}
	}
	return b.String()
}

func structFieldString(v reflect.Value, fieldName string) string {
	field := v.FieldByName(strings.TrimSpace(fieldName))
	if !field.IsValid() {
		return ""
	}
	switch field.Kind() {
	case reflect.String:
		return strings.TrimSpace(field.String())
	case reflect.Pointer:
		if field.IsNil() || field.Elem().Kind() != reflect.String {
			return ""
		}
		return strings.TrimSpace(field.Elem().String())
	default:
		return ""
	}
}

func normalizeSnowflakeAccountIdentifier(raw string) (string, string, error) {
	accountInput := strings.TrimSpace(raw)
	if accountInput == "" {
		return "", "", httpError{status: http.StatusBadRequest, msg: "Missing snowflakeAccountIdentifier in settings"}
	}

	if strings.Contains(accountInput, "://") {
		u, err := url.Parse(accountInput)
		if err != nil {
			return "", "", httpError{status: http.StatusBadRequest, msg: "Invalid Snowflake account identifier URL"}
		}
		scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
		if scheme != "https" {
			return "", "", httpError{status: http.StatusBadRequest, msg: "Snowflake account URL must use https"}
		}
		if u.User != nil {
			return "", "", httpError{status: http.StatusBadRequest, msg: "Snowflake account URL must not include user info"}
		}
		if strings.TrimSpace(u.Port()) != "" {
			return "", "", httpError{status: http.StatusBadRequest, msg: "Snowflake account URL must not include a custom port"}
		}
		if strings.TrimSpace(u.RawQuery) != "" || strings.TrimSpace(u.Fragment) != "" {
			return "", "", httpError{status: http.StatusBadRequest, msg: "Snowflake account URL must not include query or fragment"}
		}
		path := strings.TrimSpace(strings.Trim(u.Path, "/"))
		if path != "" {
			return "", "", httpError{status: http.StatusBadRequest, msg: "Snowflake account URL must not include a path"}
		}
		accountInput = strings.TrimSpace(u.Hostname())
	}

	if strings.ContainsAny(accountInput, "/?#") {
		return "", "", httpError{status: http.StatusBadRequest, msg: "Snowflake account identifier must not include path, query, or fragment"}
	}

	host := strings.TrimSpace(strings.ToLower(strings.TrimSuffix(accountInput, ".")))
	if host == "" {
		return "", "", httpError{status: http.StatusBadRequest, msg: "Invalid Snowflake account identifier"}
	}
	if strings.Contains(host, " ") || strings.Contains(host, "..") {
		return "", "", httpError{status: http.StatusBadRequest, msg: "Invalid Snowflake account identifier"}
	}

	var accountIdentifier string
	if strings.HasSuffix(host, ".snowflakecomputing.com") {
		accountIdentifier = strings.TrimSuffix(host, ".snowflakecomputing.com")
		if accountIdentifier == "" {
			return "", "", httpError{status: http.StatusBadRequest, msg: "Invalid Snowflake account host"}
		}
		if !validSnowflakeIdentifier(accountIdentifier) {
			return "", "", httpError{status: http.StatusBadRequest, msg: "Invalid Snowflake account host"}
		}
		return accountIdentifier, host, nil
	}

	if strings.Contains(host, ".snowflakecomputing") {
		return "", "", httpError{status: http.StatusBadRequest, msg: "Invalid Snowflake account host"}
	}
	if host == "snowflakecomputing.com" {
		return "", "", httpError{status: http.StatusBadRequest, msg: "Invalid Snowflake account identifier"}
	}
	if !validSnowflakeIdentifier(host) {
		return "", "", httpError{status: http.StatusBadRequest, msg: "Invalid Snowflake account identifier"}
	}
	return host, host + ".snowflakecomputing.com", nil
}

func validSnowflakeIdentifier(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return false
	}
	parts := strings.Split(value, ".")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if !snowflakeIdentifierPartRE.MatchString(part) {
			return false
		}
	}
	return true
}

func (h *Handler) keypairJWT(accountIdentifier, user, privateKeyPEM, passphrase string) (string, error) {
	accountIdentifier = strings.ToUpper(strings.TrimSpace(accountIdentifier))
	user = strings.ToUpper(strings.TrimSpace(user))
	privateKeyPEM = strings.TrimSpace(privateKeyPEM)
	passphrase = strings.TrimSpace(passphrase)
	if accountIdentifier == "" {
		return "", errors.New("Snowflake account identifier is required for keypair_jwt")
	}
	if user == "" {
		return "", errors.New("Snowflake user is required for keypair_jwt")
	}
	if privateKeyPEM == "" {
		return "", errors.New("Snowflake private key PEM is required for keypair_jwt")
	}

	cacheKeyHash := sha256.Sum256([]byte(strings.Join([]string{accountIdentifier, user, privateKeyPEM, passphrase}, "\x00")))
	cacheKey := base64.RawURLEncoding.EncodeToString(cacheKeyHash[:])
	now := h.now()

	h.tokenMu.Lock()
	if h.cachedJWT != "" && h.cachedJWTKey == cacheKey && now.Before(h.cachedJWTUntil.Add(-snowflakeJWTRefreshSkew)) {
		tok := h.cachedJWT
		h.tokenMu.Unlock()
		return tok, nil
	}
	h.tokenMu.Unlock()

	key, err := parseRSAPrivateKeyPEM(privateKeyPEM, passphrase)
	if err != nil {
		return "", err
	}
	fingerprint, err := snowflakePublicKeyFingerprint(&key.PublicKey)
	if err != nil {
		return "", fmt.Errorf("failed to compute Snowflake public key fingerprint: %w", err)
	}

	sub := accountIdentifier + "." + user
	iss := sub + "." + fingerprint
	iat := now.UTC().Unix()
	exp := now.Add(snowflakeJWTLifetime).UTC().Unix()
	if exp <= iat {
		return "", errors.New("failed to build Snowflake JWT claims")
	}

	tok, err := signRS256JWT(map[string]any{
		"iss": iss,
		"sub": sub,
		"iat": iat,
		"exp": exp,
	}, key)
	if err != nil {
		return "", fmt.Errorf("failed to sign Snowflake keypair_jwt token: %w", err)
	}

	h.tokenMu.Lock()
	h.cachedJWT = tok
	h.cachedJWTKey = cacheKey
	h.cachedJWTUntil = time.Unix(exp, 0)
	h.tokenMu.Unlock()
	return tok, nil
}

func parseRSAPrivateKeyPEM(privateKeyPEM, passphrase string) (*rsa.PrivateKey, error) {
	block, rest := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return nil, errors.New("Snowflake private key PEM is invalid")
	}
	if strings.TrimSpace(string(rest)) != "" {
		return nil, errors.New("Snowflake private key PEM must contain exactly one PEM block")
	}

	der := block.Bytes
	if x509.IsEncryptedPEMBlock(block) {
		if strings.TrimSpace(passphrase) == "" {
			return nil, errors.New("Snowflake private key PEM requires a passphrase")
		}
		decrypted, err := x509.DecryptPEMBlock(block, []byte(passphrase))
		if err != nil {
			return nil, errors.New("Snowflake private key passphrase is invalid")
		}
		der = decrypted
	} else if strings.Contains(strings.ToUpper(strings.TrimSpace(block.Type)), "ENCRYPTED") {
		return nil, errors.New("Snowflake encrypted private key PEM format is not supported")
	}

	if key, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		if key.N.BitLen() < 2048 {
			return nil, errors.New("Snowflake private key must be at least 2048 bits")
		}
		return key, nil
	}
	pkcs8Key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, errors.New("Snowflake private key PEM must be PKCS#1 or PKCS#8 RSA")
	}
	key, ok := pkcs8Key.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("Snowflake private key PEM must contain an RSA private key")
	}
	if key.N.BitLen() < 2048 {
		return nil, errors.New("Snowflake private key must be at least 2048 bits")
	}
	return key, nil
}

func snowflakePublicKeyFingerprint(pub *rsa.PublicKey) (string, error) {
	if pub == nil {
		return "", errors.New("public key is required")
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(der)
	return "SHA256:" + base64.StdEncoding.EncodeToString(sum[:]), nil
}

func signRS256JWT(claims map[string]any, key *rsa.PrivateKey) (string, error) {
	if key == nil {
		return "", errors.New("private key is required")
	}
	headerPart, err := json.Marshal(map[string]any{
		"alg": "RS256",
		"typ": "JWT",
	})
	if err != nil {
		return "", err
	}
	claimsPart, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encodedHeader := base64.RawURLEncoding.EncodeToString(headerPart)
	encodedClaims := base64.RawURLEncoding.EncodeToString(claimsPart)
	signingInput := encodedHeader + "." + encodedClaims

	hash := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
