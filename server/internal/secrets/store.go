package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ai-analyst-local/internal/atomicfile"
	"ai-analyst-local/internal/secrets/argon2"
)

type KDFParams struct {
	Name string `json:"name"`
	T    uint32 `json:"t"`
	M    uint32 `json:"m"`
	P    uint8  `json:"p"`

	SaltB64 string `json:"salt_b64"`
}

type CipherParams struct {
	Name string `json:"name"`

	NonceB64      string `json:"nonce_b64"`
	CiphertextB64 string `json:"ciphertext_b64"`
}

type Envelope struct {
	Version int          `json:"version"`
	KDF     KDFParams    `json:"kdf"`
	Cipher  CipherParams `json:"cipher"`
}

type CompatibleSecrets struct {
	APIKey        string            `json:"api_key,omitempty"`
	SecretHeaders map[string]string `json:"secret_headers,omitempty"` // header key -> value
}

type BedrockSecrets struct {
	APIKey          string            `json:"api_key,omitempty"`
	AccessKeyID     string            `json:"access_key_id,omitempty"`
	SecretAccessKey string            `json:"secret_access_key,omitempty"`
	SessionToken    string            `json:"session_token,omitempty"`
	SecretHeaders   map[string]string `json:"secret_headers,omitempty"` // header key -> value
}

type BigQuerySecrets struct {
	Mode             string `json:"mode,omitempty"` // "adc" | "oauth_token"
	OAuthAccessToken string `json:"oauth_access_token,omitempty"`
	// service_account_json could be added later.
}

type DatabricksSecrets struct {
	Mode         string `json:"mode,omitempty"` // "refresh_token" | "pat"
	RefreshToken string `json:"refresh_token,omitempty"`
	ClientID     string `json:"client_id,omitempty"` // OAuth client_id used for refresh-token exchange
	PAT          string `json:"pat,omitempty"`
}

type TrinoSecrets struct {
	Mode     string `json:"mode,omitempty"` // "basic" | "jwt" | "none"
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	JWT      string `json:"jwt,omitempty"`
}

type SnowflakeSecrets struct {
	Mode                 string `json:"mode,omitempty"` // "oauth_token" | "keypair_jwt"
	OAuthToken           string `json:"oauth_token,omitempty"`
	PrivateKeyPEM        string `json:"private_key_pem,omitempty"`
	PrivateKeyPassphrase string `json:"private_key_passphrase,omitempty"`
}

type GitHubSecrets struct {
	OAuthAccessToken string `json:"oauth_access_token,omitempty"`
	Login            string `json:"login,omitempty"`
	Scope            string `json:"scope,omitempty"`
	TokenType        string `json:"token_type,omitempty"`
	StoredAt         int64  `json:"stored_at,omitempty"`
}

type SlackSecrets struct {
	ClientSecret    string `json:"client_secret,omitempty"`
	AccessToken     string `json:"access_token,omitempty"`
	RefreshToken    string `json:"refresh_token,omitempty"`
	ExpiresAt       int64  `json:"expires_at,omitempty"`
	TokenType       string `json:"token_type,omitempty"`
	Scope           string `json:"scope,omitempty"`
	TeamID          string `json:"team_id,omitempty"`
	TeamName        string `json:"team_name,omitempty"`
	EnterpriseID    string `json:"enterprise_id,omitempty"`
	AuthedUserID    string `json:"authed_user_id,omitempty"`
	BotAccessToken  string `json:"bot_access_token,omitempty"`
	BotRefreshToken string `json:"bot_refresh_token,omitempty"`
	BotExpiresAt    int64  `json:"bot_expires_at,omitempty"`
	BotTokenType    string `json:"bot_token_type,omitempty"`
	BotScope        string `json:"bot_scope,omitempty"`
	BotUserID       string `json:"bot_user_id,omitempty"`
	BotID           string `json:"bot_id,omitempty"`
	InstalledAt     int64  `json:"installed_at,omitempty"`
	LastRefreshed   int64  `json:"last_refreshed,omitempty"`
}

type Plaintext struct {
	OpenAIAPIKey    string                       `json:"openai_api_key,omitempty"`
	AnthropicAPIKey string                       `json:"anthropic_api_key,omitempty"`
	GeminiAPIKey    string                       `json:"gemini_api_key,omitempty"`
	Bedrock         BedrockSecrets               `json:"bedrock,omitempty"`
	Compatible      map[string]CompatibleSecrets `json:"compatible,omitempty"` // profileId -> secrets
	BigQuery        BigQuerySecrets              `json:"bigquery,omitempty"`
	Databricks      DatabricksSecrets            `json:"databricks,omitempty"`
	Trino           TrinoSecrets                 `json:"trino,omitempty"`
	Snowflake       SnowflakeSecrets             `json:"snowflake,omitempty"`
	GitHub          GitHubSecrets                `json:"github,omitempty"`
	Slack           SlackSecrets                 `json:"slack,omitempty"`
}

type ConfiguredStatus struct {
	OpenAI                 bool                            `json:"openai"`
	Anthropic              bool                            `json:"anthropic"`
	Gemini                 bool                            `json:"gemini"`
	Bedrock                bool                            `json:"bedrock"`
	BedrockAPIKey          bool                            `json:"bedrockApiKey,omitempty"`
	BedrockAWSCredentials  bool                            `json:"bedrockAwsCredentials,omitempty"`
	Compatible             map[string]CompatibleConfigured `json:"compatible"` // profileId -> status
	BigQuery               bool                            `json:"bigquery"`
	Databricks             bool                            `json:"databricks"`
	DatabricksRefreshToken bool                            `json:"databricksRefreshToken,omitempty"`
	DatabricksPAT          bool                            `json:"databricksPat,omitempty"`
	Trino                  bool                            `json:"trino"`
	TrinoBasicAuth         bool                            `json:"trinoBasicAuth,omitempty"`
	TrinoJWT               bool                            `json:"trinoJwt,omitempty"`
	TrinoNone              bool                            `json:"trinoNone,omitempty"`
	Snowflake              bool                            `json:"snowflake"`
	SnowflakeOAuthToken    bool                            `json:"snowflakeOAuthToken"`
	SnowflakeKeyPair       bool                            `json:"snowflakeKeyPair"`
	GitHub                 bool                            `json:"github"`
	GitHubLogin            string                          `json:"githubLogin,omitempty"`
	Slack                  bool                            `json:"slack"`
	SlackUser              bool                            `json:"slackUser,omitempty"`
	SlackBot               bool                            `json:"slackBot,omitempty"`
	SlackClientSecret      bool                            `json:"slackClientSecret,omitempty"`
	SlackTeamID            string                          `json:"slackTeamId,omitempty"`
	SlackTeamName          string                          `json:"slackTeamName,omitempty"`
	SlackScope             string                          `json:"slackScope,omitempty"`
	SlackBotScope          string                          `json:"slackBotScope,omitempty"`
	SlackAuthedUserID      string                          `json:"slackAuthedUserId,omitempty"`
	SlackBotUserID         string                          `json:"slackBotUserId,omitempty"`
}

type CompatibleConfigured struct {
	APIKey           bool     `json:"apiKey"`
	SecretHeaderKeys []string `json:"secretHeaderKeys,omitempty"`
}

type Store struct {
	mu sync.Mutex

	path string

	initialized bool
	locked      bool
	lastUsed    time.Time
	idleLock    time.Duration

	kdf KDFParams
	key []byte // derived encryption key (kept in memory while unlocked)

	plain Plaintext
}

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, "secrets.v1.json.enc")
	st := &Store{
		path:     p,
		locked:   true,
		idleLock: 30 * time.Minute,
		plain:    defaultPlaintext(),
		lastUsed: time.Now(),
	}
	if _, err := os.Stat(p); err == nil {
		st.initialized = true
	} else if os.IsNotExist(err) {
		st.initialized = false
	} else {
		return nil, err
	}
	return st, nil
}

func (s *Store) Path() string { return s.path }

func (s *Store) Status() (initialized bool, locked bool, configured ConfiguredStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.maybeAutoLockLocked()

	bedrockHasAPIKey := strings.TrimSpace(s.plain.Bedrock.APIKey) != ""
	bedrockHasAWSCreds := strings.TrimSpace(s.plain.Bedrock.AccessKeyID) != "" && strings.TrimSpace(s.plain.Bedrock.SecretAccessKey) != ""
	slackUserSet := strings.TrimSpace(s.plain.Slack.AccessToken) != ""
	slackBotSet := strings.TrimSpace(s.plain.Slack.BotAccessToken) != ""
	cfg := ConfiguredStatus{
		OpenAI:                strings.TrimSpace(s.plain.OpenAIAPIKey) != "",
		Anthropic:             strings.TrimSpace(s.plain.AnthropicAPIKey) != "",
		Gemini:                strings.TrimSpace(s.plain.GeminiAPIKey) != "",
		Bedrock:               bedrockHasAPIKey || bedrockHasAWSCreds,
		BedrockAPIKey:         bedrockHasAPIKey,
		BedrockAWSCredentials: bedrockHasAWSCreds,
		Compatible:            map[string]CompatibleConfigured{},
		BigQuery:              strings.TrimSpace(s.plain.BigQuery.OAuthAccessToken) != "",
		Databricks:            false,
		Trino:                 false,
		Snowflake:             false,
		GitHub:                strings.TrimSpace(s.plain.GitHub.OAuthAccessToken) != "",
		GitHubLogin:           strings.TrimSpace(s.plain.GitHub.Login),
		Slack:                 slackUserSet || slackBotSet,
		SlackUser:             slackUserSet,
		SlackBot:              slackBotSet,
		SlackClientSecret:     strings.TrimSpace(s.plain.Slack.ClientSecret) != "",
		SlackTeamID:           strings.TrimSpace(s.plain.Slack.TeamID),
		SlackTeamName:         strings.TrimSpace(s.plain.Slack.TeamName),
		SlackScope:            strings.TrimSpace(s.plain.Slack.Scope),
		SlackBotScope:         strings.TrimSpace(s.plain.Slack.BotScope),
		SlackAuthedUserID:     strings.TrimSpace(s.plain.Slack.AuthedUserID),
		SlackBotUserID:        strings.TrimSpace(s.plain.Slack.BotUserID),
	}
	dbRefresh := strings.TrimSpace(s.plain.Databricks.RefreshToken) != ""
	dbPAT := strings.TrimSpace(s.plain.Databricks.PAT) != ""
	cfg.Databricks = dbRefresh || dbPAT
	cfg.DatabricksRefreshToken = dbRefresh
	cfg.DatabricksPAT = dbPAT

	trinoMode := strings.TrimSpace(strings.ToLower(s.plain.Trino.Mode))
	trinoUserSet := strings.TrimSpace(s.plain.Trino.Username) != ""
	trinoPassSet := strings.TrimSpace(s.plain.Trino.Password) != ""
	trinoJWTSet := strings.TrimSpace(s.plain.Trino.JWT) != ""
	if trinoMode == "" {
		switch {
		case trinoUserSet && trinoPassSet:
			trinoMode = "basic"
		case trinoJWTSet:
			trinoMode = "jwt"
		}
	}
	cfg.TrinoBasicAuth = trinoMode == "basic" && trinoUserSet && trinoPassSet
	cfg.TrinoJWT = trinoMode == "jwt" && trinoJWTSet
	cfg.TrinoNone = trinoMode == "none"
	cfg.Trino = cfg.TrinoBasicAuth || cfg.TrinoJWT || cfg.TrinoNone

	snowflakeMode := strings.TrimSpace(strings.ToLower(s.plain.Snowflake.Mode))
	snowflakeOAuthTokenSet := strings.TrimSpace(s.plain.Snowflake.OAuthToken) != ""
	snowflakeKeyPairSet := strings.TrimSpace(s.plain.Snowflake.PrivateKeyPEM) != ""
	if snowflakeMode == "" {
		switch {
		case snowflakeKeyPairSet:
			snowflakeMode = "keypair_jwt"
		case snowflakeOAuthTokenSet:
			snowflakeMode = "oauth_token"
		}
	}
	cfg.SnowflakeOAuthToken = snowflakeMode == "oauth_token" && snowflakeOAuthTokenSet
	cfg.SnowflakeKeyPair = snowflakeMode == "keypair_jwt" && snowflakeKeyPairSet
	cfg.Snowflake = cfg.SnowflakeOAuthToken || cfg.SnowflakeKeyPair

	for pid, cs := range s.plain.Compatible {
		keys := make([]string, 0, len(cs.SecretHeaders))
		for k := range cs.SecretHeaders {
			if strings.TrimSpace(k) == "" {
				continue
			}
			keys = append(keys, k)
		}
		cfg.Compatible[pid] = CompatibleConfigured{
			APIKey:           strings.TrimSpace(cs.APIKey) != "",
			SecretHeaderKeys: keys,
		}
	}

	return s.initialized, s.locked, cfg
}

func (s *Store) Init(passphrase string) error {
	pp := strings.TrimSpace(passphrase)
	if pp == "" {
		return errors.New("passphrase is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.initialized {
		return errors.New("secrets store is already initialized")
	}

	kdf, err := defaultKDF()
	if err != nil {
		return err
	}
	env, err := encryptEnvelopeWithPassphrase([]byte("{}"), pp, kdf)
	if err != nil {
		return err
	}
	if err := writeEnvelopeFile(s.path, env); err != nil {
		return err
	}
	s.initialized = true
	s.locked = true
	return nil
}

func (s *Store) Unlock(passphrase string) error {
	pp := strings.TrimSpace(passphrase)
	if pp == "" {
		return errors.New("passphrase is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.initialized {
		return errors.New("secrets store is not initialized")
	}

	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var env Envelope
	if err := json.Unmarshal(b, &env); err != nil {
		return err
	}
	plainBytes, kdf, key, err := decryptEnvelopeWithPassphrase(env, pp)
	if err != nil {
		return errors.New("wrong passphrase or corrupted secrets file")
	}
	var plain Plaintext
	if err := json.Unmarshal(plainBytes, &plain); err != nil {
		return err
	}
	if plain.Compatible == nil {
		plain.Compatible = map[string]CompatibleSecrets{}
	}
	if plain.Bedrock.SecretHeaders == nil {
		plain.Bedrock.SecretHeaders = map[string]string{}
	}
	s.plain = plain
	s.kdf = kdf
	s.key = key
	s.locked = false
	s.lastUsed = time.Now()
	return nil
}

func (s *Store) Lock() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plain = defaultPlaintext()
	if s.key != nil {
		for i := range s.key {
			s.key[i] = 0
		}
	}
	s.key = nil
	s.locked = true
	s.lastUsed = time.Now()
}

func (s *Store) SetIdleLock(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d <= 0 {
		s.idleLock = 0
		return
	}
	s.idleLock = d
}

func (s *Store) maybeAutoLockLocked() {
	if s.locked {
		return
	}
	if s.idleLock <= 0 {
		return
	}
	if time.Since(s.lastUsed) >= s.idleLock {
		s.plain = defaultPlaintext()
		s.locked = true
	}
}

func (s *Store) touch() {
	s.lastUsed = time.Now()
}

func (s *Store) requireUnlocked() error {
	if !s.initialized {
		return errors.New("secrets store is not initialized")
	}
	if s.locked {
		return errors.New("secrets store is locked")
	}
	s.touch()
	return nil
}

func (s *Store) SetProviderKey(providerID string, apiKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	switch providerID {
	case "openai":
		s.plain.OpenAIAPIKey = strings.TrimSpace(apiKey)
	case "anthropic":
		s.plain.AnthropicAPIKey = strings.TrimSpace(apiKey)
	case "gemini":
		s.plain.GeminiAPIKey = strings.TrimSpace(apiKey)
	default:
		return fmt.Errorf("unknown providerId: %s", providerID)
	}
	return s.saveUnlocked()
}

func (s *Store) ClearProviderKey(providerID string) error {
	return s.SetProviderKey(providerID, "")
}

func (s *Store) SetCompatibleAPIKey(profileID string, apiKey string) error {
	pid := strings.TrimSpace(profileID)
	if pid == "" {
		return errors.New("profileId is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	if s.plain.Compatible == nil {
		s.plain.Compatible = map[string]CompatibleSecrets{}
	}
	cur := s.plain.Compatible[pid]
	cur.APIKey = strings.TrimSpace(apiKey)
	if cur.SecretHeaders == nil {
		cur.SecretHeaders = map[string]string{}
	}
	s.plain.Compatible[pid] = cur
	return s.saveUnlocked()
}

func (s *Store) ClearCompatibleAPIKey(profileID string) error {
	return s.SetCompatibleAPIKey(profileID, "")
}

func (s *Store) SetCompatibleSecretHeader(profileID string, headerKey string, headerValue string) error {
	pid := strings.TrimSpace(profileID)
	if pid == "" {
		return errors.New("profileId is required")
	}
	key := strings.TrimSpace(headerKey)
	if key == "" {
		return errors.New("headerKey is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	if s.plain.Compatible == nil {
		s.plain.Compatible = map[string]CompatibleSecrets{}
	}
	cur := s.plain.Compatible[pid]
	if cur.SecretHeaders == nil {
		cur.SecretHeaders = map[string]string{}
	}
	if strings.TrimSpace(headerValue) == "" {
		delete(cur.SecretHeaders, key)
	} else {
		cur.SecretHeaders[key] = headerValue
	}
	s.plain.Compatible[pid] = cur
	return s.saveUnlocked()
}

func (s *Store) SetBigQueryOAuthAccessToken(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	s.plain.BigQuery.Mode = "oauth_token"
	s.plain.BigQuery.OAuthAccessToken = strings.TrimSpace(token)
	return s.saveUnlocked()
}

func (s *Store) ClearBigQueryOAuthAccessToken() error {
	return s.SetBigQueryOAuthAccessToken("")
}

func (s *Store) SetBedrockCredentials(accessKeyID string, secretAccessKey string, sessionToken string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	s.plain.Bedrock.APIKey = ""
	s.plain.Bedrock.AccessKeyID = strings.TrimSpace(accessKeyID)
	s.plain.Bedrock.SecretAccessKey = strings.TrimSpace(secretAccessKey)
	s.plain.Bedrock.SessionToken = strings.TrimSpace(sessionToken)
	if s.plain.Bedrock.SecretHeaders == nil {
		s.plain.Bedrock.SecretHeaders = map[string]string{}
	}
	return s.saveUnlocked()
}

func (s *Store) ClearBedrockCredentials() error {
	return s.SetBedrockCredentials("", "", "")
}

func (s *Store) SetBedrockAPIKey(apiKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	s.plain.Bedrock.APIKey = strings.TrimSpace(apiKey)
	s.plain.Bedrock.AccessKeyID = ""
	s.plain.Bedrock.SecretAccessKey = ""
	s.plain.Bedrock.SessionToken = ""
	if s.plain.Bedrock.SecretHeaders == nil {
		s.plain.Bedrock.SecretHeaders = map[string]string{}
	}
	return s.saveUnlocked()
}

func (s *Store) ClearBedrockAPIKey() error {
	return s.SetBedrockAPIKey("")
}

func (s *Store) ClearBedrockAuth() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	s.plain.Bedrock.APIKey = ""
	s.plain.Bedrock.AccessKeyID = ""
	s.plain.Bedrock.SecretAccessKey = ""
	s.plain.Bedrock.SessionToken = ""
	if s.plain.Bedrock.SecretHeaders == nil {
		s.plain.Bedrock.SecretHeaders = map[string]string{}
	}
	return s.saveUnlocked()
}

func (s *Store) SetBedrockSecretHeader(headerKey string, headerValue string) error {
	key := strings.TrimSpace(headerKey)
	if key == "" {
		return errors.New("headerKey is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	if s.plain.Bedrock.SecretHeaders == nil {
		s.plain.Bedrock.SecretHeaders = map[string]string{}
	}
	if strings.TrimSpace(headerValue) == "" {
		delete(s.plain.Bedrock.SecretHeaders, key)
	} else {
		s.plain.Bedrock.SecretHeaders[key] = headerValue
	}
	return s.saveUnlocked()
}

func (s *Store) GetProviderKey(providerID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maybeAutoLockLocked()
	if err := s.requireUnlocked(); err != nil {
		return "", err
	}
	switch providerID {
	case "openai":
		return s.plain.OpenAIAPIKey, nil
	case "anthropic":
		return s.plain.AnthropicAPIKey, nil
	case "gemini":
		return s.plain.GeminiAPIKey, nil
	default:
		return "", fmt.Errorf("unknown providerId: %s", providerID)
	}
}

func (s *Store) GetCompatible(profileID string) (CompatibleSecrets, error) {
	pid := strings.TrimSpace(profileID)
	if pid == "" {
		return CompatibleSecrets{}, errors.New("profileId is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maybeAutoLockLocked()
	if err := s.requireUnlocked(); err != nil {
		return CompatibleSecrets{}, err
	}
	cur, ok := s.plain.Compatible[pid]
	if !ok {
		cur = CompatibleSecrets{SecretHeaders: map[string]string{}}
	}
	if cur.SecretHeaders == nil {
		cur.SecretHeaders = map[string]string{}
	}
	return cur, nil
}

func (s *Store) GetBedrock() (BedrockSecrets, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maybeAutoLockLocked()
	if err := s.requireUnlocked(); err != nil {
		return BedrockSecrets{}, err
	}
	out := s.plain.Bedrock
	if out.SecretHeaders == nil {
		out.SecretHeaders = map[string]string{}
	}
	return out, nil
}

func (s *Store) GetBigQueryToken() (mode string, token string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maybeAutoLockLocked()
	if err := s.requireUnlocked(); err != nil {
		return "", "", err
	}
	mode = strings.TrimSpace(s.plain.BigQuery.Mode)
	token = strings.TrimSpace(s.plain.BigQuery.OAuthAccessToken)
	if mode == "" {
		mode = "adc"
	}
	return mode, token, nil
}

func (s *Store) SetDatabricksRefreshToken(refreshToken string) error {
	return s.SetDatabricksRefreshTokenWithClientID(refreshToken, "databricks-cli")
}

func (s *Store) SetDatabricksRefreshTokenWithClientID(refreshToken string, clientID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	rt := strings.TrimSpace(refreshToken)
	if rt == "" {
		s.plain.Databricks.Mode = ""
		s.plain.Databricks.RefreshToken = ""
		s.plain.Databricks.ClientID = ""
		s.plain.Databricks.PAT = ""
		return s.saveUnlocked()
	}
	cid := strings.TrimSpace(clientID)
	if cid == "" {
		cid = "databricks-cli"
	}
	s.plain.Databricks.Mode = "refresh_token"
	s.plain.Databricks.RefreshToken = rt
	s.plain.Databricks.ClientID = cid
	s.plain.Databricks.PAT = ""
	return s.saveUnlocked()
}

func (s *Store) SetDatabricksPAT(pat string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	p := strings.TrimSpace(pat)
	if p == "" {
		s.plain.Databricks.Mode = ""
		s.plain.Databricks.PAT = ""
		s.plain.Databricks.RefreshToken = ""
		s.plain.Databricks.ClientID = ""
		return s.saveUnlocked()
	}
	s.plain.Databricks.Mode = "pat"
	s.plain.Databricks.PAT = p
	s.plain.Databricks.RefreshToken = ""
	s.plain.Databricks.ClientID = ""
	return s.saveUnlocked()
}

func (s *Store) ClearDatabricks(authType string) error {
	typ := strings.TrimSpace(strings.ToLower(authType))
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	switch typ {
	case "", "all":
		s.plain.Databricks.Mode = ""
		s.plain.Databricks.RefreshToken = ""
		s.plain.Databricks.ClientID = ""
		s.plain.Databricks.PAT = ""
	case "refresh_token":
		s.plain.Databricks.RefreshToken = ""
		s.plain.Databricks.ClientID = ""
		if strings.EqualFold(strings.TrimSpace(s.plain.Databricks.Mode), "refresh_token") {
			s.plain.Databricks.Mode = ""
		}
	case "pat":
		s.plain.Databricks.PAT = ""
		if strings.EqualFold(strings.TrimSpace(s.plain.Databricks.Mode), "pat") {
			s.plain.Databricks.Mode = ""
		}
	default:
		return fmt.Errorf("unknown databricks authType: %s", authType)
	}
	return s.saveUnlocked()
}

func (s *Store) GetDatabricks() (DatabricksSecrets, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maybeAutoLockLocked()
	if err := s.requireUnlocked(); err != nil {
		return DatabricksSecrets{}, err
	}
	out := s.plain.Databricks
	out.Mode = strings.TrimSpace(out.Mode)
	out.RefreshToken = strings.TrimSpace(out.RefreshToken)
	out.ClientID = strings.TrimSpace(out.ClientID)
	out.PAT = strings.TrimSpace(out.PAT)
	if out.ClientID == "" {
		out.ClientID = "databricks-cli"
	}
	return out, nil
}

func (s *Store) SetTrinoBasicAuth(username string, password string) error {
	u := strings.TrimSpace(username)
	p := strings.TrimSpace(password)
	if u == "" {
		return errors.New("username is required for trino basic auth")
	}
	if p == "" {
		return errors.New("password is required for trino basic auth")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	s.plain.Trino.Mode = "basic"
	s.plain.Trino.Username = u
	s.plain.Trino.Password = p
	s.plain.Trino.JWT = ""
	return s.saveUnlocked()
}

func (s *Store) SetTrinoJWT(token string) error {
	j := strings.TrimSpace(token)
	if j == "" {
		return errors.New("jwt is required for trino jwt auth")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	s.plain.Trino.Mode = "jwt"
	s.plain.Trino.JWT = j
	s.plain.Trino.Username = ""
	s.plain.Trino.Password = ""
	return s.saveUnlocked()
}

func (s *Store) SetTrinoNone() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	s.plain.Trino.Mode = "none"
	s.plain.Trino.Username = ""
	s.plain.Trino.Password = ""
	s.plain.Trino.JWT = ""
	return s.saveUnlocked()
}

func (s *Store) ClearTrino(authType string) error {
	typ := strings.TrimSpace(strings.ToLower(authType))
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	switch typ {
	case "", "all":
		s.plain.Trino.Mode = ""
		s.plain.Trino.Username = ""
		s.plain.Trino.Password = ""
		s.plain.Trino.JWT = ""
	case "basic":
		s.plain.Trino.Username = ""
		s.plain.Trino.Password = ""
		if strings.EqualFold(strings.TrimSpace(s.plain.Trino.Mode), "basic") {
			if strings.TrimSpace(s.plain.Trino.JWT) != "" {
				s.plain.Trino.Mode = "jwt"
			} else {
				s.plain.Trino.Mode = ""
			}
		}
	case "jwt":
		s.plain.Trino.JWT = ""
		if strings.EqualFold(strings.TrimSpace(s.plain.Trino.Mode), "jwt") {
			if strings.TrimSpace(s.plain.Trino.Username) != "" && strings.TrimSpace(s.plain.Trino.Password) != "" {
				s.plain.Trino.Mode = "basic"
			} else {
				s.plain.Trino.Mode = ""
			}
		}
	default:
		return fmt.Errorf("unknown trino authType: %s", authType)
	}
	return s.saveUnlocked()
}

func (s *Store) GetTrino() (TrinoSecrets, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maybeAutoLockLocked()
	if err := s.requireUnlocked(); err != nil {
		return TrinoSecrets{}, err
	}
	out := s.plain.Trino
	out.Mode = strings.TrimSpace(strings.ToLower(out.Mode))
	out.Username = strings.TrimSpace(out.Username)
	out.Password = strings.TrimSpace(out.Password)
	out.JWT = strings.TrimSpace(out.JWT)
	if out.Mode == "" {
		switch {
		case out.Username != "" && out.Password != "":
			out.Mode = "basic"
		case out.JWT != "":
			out.Mode = "jwt"
		default:
			out.Mode = "none"
		}
	}
	return out, nil
}

func (s *Store) SetSnowflakeOAuthToken(token string) error {
	tok := strings.TrimSpace(token)
	if tok == "" {
		return errors.New("oauth token is required for snowflake oauth_token auth")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	s.plain.Snowflake.Mode = "oauth_token"
	s.plain.Snowflake.OAuthToken = tok
	s.plain.Snowflake.PrivateKeyPEM = ""
	s.plain.Snowflake.PrivateKeyPassphrase = ""
	return s.saveUnlocked()
}

func (s *Store) SetSnowflakeKeyPair(privateKeyPEM string, passphrase string) error {
	key := strings.TrimSpace(privateKeyPEM)
	if key == "" {
		return errors.New("private key pem is required for snowflake keypair_jwt auth")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	s.plain.Snowflake.Mode = "keypair_jwt"
	s.plain.Snowflake.PrivateKeyPEM = key
	s.plain.Snowflake.PrivateKeyPassphrase = strings.TrimSpace(passphrase)
	s.plain.Snowflake.OAuthToken = ""
	return s.saveUnlocked()
}

func (s *Store) ClearSnowflake() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	s.plain.Snowflake.Mode = ""
	s.plain.Snowflake.OAuthToken = ""
	s.plain.Snowflake.PrivateKeyPEM = ""
	s.plain.Snowflake.PrivateKeyPassphrase = ""
	return s.saveUnlocked()
}

func (s *Store) GetSnowflake() (SnowflakeSecrets, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maybeAutoLockLocked()
	if err := s.requireUnlocked(); err != nil {
		return SnowflakeSecrets{}, err
	}
	out := s.plain.Snowflake
	out.Mode = strings.TrimSpace(strings.ToLower(out.Mode))
	out.OAuthToken = strings.TrimSpace(out.OAuthToken)
	out.PrivateKeyPEM = strings.TrimSpace(out.PrivateKeyPEM)
	out.PrivateKeyPassphrase = strings.TrimSpace(out.PrivateKeyPassphrase)
	switch out.Mode {
	case "oauth_token", "keypair_jwt":
	default:
		if out.PrivateKeyPEM != "" {
			out.Mode = "keypair_jwt"
		} else {
			out.Mode = "oauth_token"
		}
	}
	return out, nil
}

func (s *Store) SetGitHubOAuthAccessToken(token string, login string, scope string, tokenType string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	tok := strings.TrimSpace(token)
	if tok == "" {
		s.plain.GitHub = GitHubSecrets{}
		return s.saveUnlocked()
	}
	typ := strings.TrimSpace(tokenType)
	if typ == "" {
		typ = "bearer"
	}
	s.plain.GitHub = GitHubSecrets{
		OAuthAccessToken: tok,
		Login:            strings.TrimSpace(login),
		Scope:            strings.TrimSpace(scope),
		TokenType:        typ,
		StoredAt:         time.Now().Unix(),
	}
	return s.saveUnlocked()
}

func (s *Store) ClearGitHubOAuthAccessToken() error {
	return s.SetGitHubOAuthAccessToken("", "", "", "")
}

func (s *Store) GetGitHub() (GitHubSecrets, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maybeAutoLockLocked()
	if err := s.requireUnlocked(); err != nil {
		return GitHubSecrets{}, err
	}
	out := s.plain.GitHub
	out.OAuthAccessToken = strings.TrimSpace(out.OAuthAccessToken)
	out.Login = strings.TrimSpace(out.Login)
	out.Scope = strings.TrimSpace(out.Scope)
	out.TokenType = strings.TrimSpace(out.TokenType)
	if out.TokenType == "" {
		out.TokenType = "bearer"
	}
	return out, nil
}

func (s *Store) SetSlackOAuth(sec SlackSecrets) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	clientSecret := strings.TrimSpace(sec.ClientSecret)
	if clientSecret == "" {
		clientSecret = strings.TrimSpace(s.plain.Slack.ClientSecret)
	}
	access := strings.TrimSpace(sec.AccessToken)
	botAccess := strings.TrimSpace(sec.BotAccessToken)
	if access == "" && botAccess == "" {
		s.plain.Slack = SlackSecrets{ClientSecret: clientSecret}
		return s.saveUnlocked()
	}
	tokenType := strings.TrimSpace(sec.TokenType)
	if tokenType == "" {
		tokenType = "bearer"
	}
	botTokenType := strings.TrimSpace(sec.BotTokenType)
	if botTokenType == "" && botAccess != "" {
		botTokenType = "bearer"
	}
	now := time.Now().Unix()
	installedAt := sec.InstalledAt
	if installedAt <= 0 {
		installedAt = s.plain.Slack.InstalledAt
	}
	if installedAt <= 0 {
		installedAt = now
	}
	lastRefreshed := sec.LastRefreshed
	if lastRefreshed <= 0 {
		lastRefreshed = now
	}
	s.plain.Slack = SlackSecrets{
		ClientSecret:    clientSecret,
		AccessToken:     access,
		RefreshToken:    strings.TrimSpace(sec.RefreshToken),
		ExpiresAt:       sec.ExpiresAt,
		TokenType:       tokenType,
		Scope:           strings.TrimSpace(sec.Scope),
		TeamID:          strings.TrimSpace(sec.TeamID),
		TeamName:        strings.TrimSpace(sec.TeamName),
		EnterpriseID:    strings.TrimSpace(sec.EnterpriseID),
		AuthedUserID:    strings.TrimSpace(sec.AuthedUserID),
		BotAccessToken:  botAccess,
		BotRefreshToken: strings.TrimSpace(sec.BotRefreshToken),
		BotExpiresAt:    sec.BotExpiresAt,
		BotTokenType:    botTokenType,
		BotScope:        strings.TrimSpace(sec.BotScope),
		BotUserID:       strings.TrimSpace(sec.BotUserID),
		BotID:           strings.TrimSpace(sec.BotID),
		InstalledAt:     installedAt,
		LastRefreshed:   lastRefreshed,
	}
	return s.saveUnlocked()
}

func (s *Store) ClearSlackOAuth() error {
	return s.SetSlackOAuth(SlackSecrets{})
}

func (s *Store) SetSlackClientSecret(clientSecret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	s.plain.Slack.ClientSecret = strings.TrimSpace(clientSecret)
	return s.saveUnlocked()
}

func (s *Store) ClearSlackClientSecret() error {
	return s.SetSlackClientSecret("")
}

func (s *Store) GetSlack() (SlackSecrets, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maybeAutoLockLocked()
	if err := s.requireUnlocked(); err != nil {
		return SlackSecrets{}, err
	}
	out := s.plain.Slack
	out.ClientSecret = strings.TrimSpace(out.ClientSecret)
	out.AccessToken = strings.TrimSpace(out.AccessToken)
	out.RefreshToken = strings.TrimSpace(out.RefreshToken)
	out.TokenType = strings.TrimSpace(out.TokenType)
	if out.TokenType == "" && out.AccessToken != "" {
		out.TokenType = "bearer"
	}
	out.Scope = strings.TrimSpace(out.Scope)
	out.TeamID = strings.TrimSpace(out.TeamID)
	out.TeamName = strings.TrimSpace(out.TeamName)
	out.EnterpriseID = strings.TrimSpace(out.EnterpriseID)
	out.AuthedUserID = strings.TrimSpace(out.AuthedUserID)
	out.BotAccessToken = strings.TrimSpace(out.BotAccessToken)
	out.BotRefreshToken = strings.TrimSpace(out.BotRefreshToken)
	out.BotTokenType = strings.TrimSpace(out.BotTokenType)
	if out.BotTokenType == "" && out.BotAccessToken != "" {
		out.BotTokenType = "bearer"
	}
	out.BotScope = strings.TrimSpace(out.BotScope)
	out.BotUserID = strings.TrimSpace(out.BotUserID)
	out.BotID = strings.TrimSpace(out.BotID)
	return out, nil
}

func (s *Store) saveLocked(passphrase string) error {
	// Deprecated path: callers should Unlock first. Kept for backward compatibility within this package.
	if strings.TrimSpace(passphrase) == "" {
		return errors.New("passphrase is required")
	}
	kdf, err := defaultKDF()
	if err != nil {
		return err
	}
	env, err := encryptEnvelopeWithPassphrase([]byte("{}"), passphrase, kdf)
	if err != nil {
		return err
	}
	return writeEnvelopeFile(s.path, env)
}

func (s *Store) saveUnlocked() error {
	if s.key == nil || len(s.key) != 32 {
		return errors.New("secrets store is not unlocked")
	}
	plainBytes, err := json.Marshal(s.plain)
	if err != nil {
		return err
	}
	env, err := encryptEnvelopeWithKey(plainBytes, s.kdf, s.key)
	if err != nil {
		return err
	}
	return writeEnvelopeFile(s.path, env)
}

func defaultKDF() (KDFParams, error) {
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return KDFParams{}, err
	}
	return KDFParams{
		Name:    "argon2id",
		T:       3,
		M:       64 * 1024,
		P:       4,
		SaltB64: base64.StdEncoding.EncodeToString(salt),
	}, nil
}

func writeEnvelopeFile(path string, env Envelope) error {
	out, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, out, 0o600)
}

func encryptEnvelopeWithPassphrase(plain []byte, passphrase string, kdf KDFParams) (Envelope, error) {
	salt, err := base64.StdEncoding.DecodeString(kdf.SaltB64)
	if err != nil {
		return Envelope{}, err
	}
	key := argon2.IDKey([]byte(passphrase), salt, kdf.T, kdf.M, kdf.P, 32)
	return encryptEnvelopeWithKey(plain, kdf, key)
}

func encryptEnvelopeWithKey(plain []byte, kdf KDFParams, key []byte) (Envelope, error) {
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return Envelope{}, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return Envelope{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return Envelope{}, err
	}
	ciphertext := gcm.Seal(nil, nonce, plain, nil)
	return Envelope{
		Version: 1,
		KDF:     kdf,
		Cipher: CipherParams{
			Name:          "aes-256-gcm",
			NonceB64:      base64.StdEncoding.EncodeToString(nonce),
			CiphertextB64: base64.StdEncoding.EncodeToString(ciphertext),
		},
	}, nil
}

func decryptEnvelopeWithPassphrase(env Envelope, passphrase string) ([]byte, KDFParams, []byte, error) {
	if env.Version != 1 {
		return nil, KDFParams{}, nil, fmt.Errorf("unsupported secrets version: %d", env.Version)
	}
	if strings.ToLower(env.KDF.Name) != "argon2id" {
		return nil, KDFParams{}, nil, fmt.Errorf("unsupported kdf: %s", env.KDF.Name)
	}
	if strings.ToLower(env.Cipher.Name) != "aes-256-gcm" {
		return nil, KDFParams{}, nil, fmt.Errorf("unsupported cipher: %s", env.Cipher.Name)
	}

	salt, err := base64.StdEncoding.DecodeString(env.KDF.SaltB64)
	if err != nil {
		return nil, KDFParams{}, nil, err
	}
	nonce, err := base64.StdEncoding.DecodeString(env.Cipher.NonceB64)
	if err != nil {
		return nil, KDFParams{}, nil, err
	}
	ciphertext, err := base64.StdEncoding.DecodeString(env.Cipher.CiphertextB64)
	if err != nil {
		return nil, KDFParams{}, nil, err
	}

	key := argon2.IDKey([]byte(passphrase), salt, env.KDF.T, env.KDF.M, env.KDF.P, 32)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, KDFParams{}, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, KDFParams{}, nil, err
	}

	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, KDFParams{}, nil, err
	}
	// Ensure JSON is reasonable (avoid confusing errors later).
	plain = bytes.TrimSpace(plain)
	if len(plain) == 0 {
		return []byte("{}"), env.KDF, key, nil
	}
	if plain[0] != '{' {
		return nil, KDFParams{}, nil, errors.New("decrypted secrets are not a JSON object")
	}
	return plain, env.KDF, key, nil
}

func defaultPlaintext() Plaintext {
	return Plaintext{
		Bedrock: BedrockSecrets{
			SecretHeaders: map[string]string{},
		},
		Compatible: map[string]CompatibleSecrets{},
	}
}
