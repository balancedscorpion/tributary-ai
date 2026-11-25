package bigquery

import (
	"context"
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
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"ai-analyst-local/internal/localexec"
)

var bqScopes = []string{"https://www.googleapis.com/auth/bigquery"}

type oauthTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type,omitempty"`
	ExpiresIn   int64  `json:"expires_in,omitempty"`

	Error            string `json:"error,omitempty"`
	ErrorDescription string `json:"error_description,omitempty"`
}

func accessTokenFromMetadata(ctx context.Context, httpc *http.Client) (string, time.Time, error) {
	// Keep this very short so local dev doesn't hang if not on GCP.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token", nil)
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Metadata-Flavor", "Google")

	resp, err := httpc.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", time.Time{}, fmt.Errorf("metadata token unavailable (HTTP %d)", resp.StatusCode)
	}

	var tr oauthTokenResponse
	if err := json.Unmarshal(b, &tr); err != nil {
		return "", time.Time{}, err
	}
	tok := strings.TrimSpace(tr.AccessToken)
	if tok == "" {
		return "", time.Time{}, errors.New("empty token from metadata server")
	}
	exp := time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	return tok, exp, nil
}

type credentialsFile struct {
	Type string `json:"type"`

	// "authorized_user"
	ClientID       string `json:"client_id,omitempty"`
	ClientSecret   string `json:"client_secret,omitempty"`
	RefreshToken   string `json:"refresh_token,omitempty"`
	QuotaProjectID string `json:"quota_project_id,omitempty"`

	// "service_account"
	ClientEmail  string `json:"client_email,omitempty"`
	PrivateKey   string `json:"private_key,omitempty"`
	PrivateKeyID string `json:"private_key_id,omitempty"`
	ProjectID    string `json:"project_id,omitempty"`
	TokenURI     string `json:"token_uri,omitempty"`
}

type credentialsPathInfo struct {
	Path   string
	Source string // google_application_credentials | cloudsdk_config | default | none
}

type detectedProjectInfo struct {
	ProjectID      string
	ProjectSource  string
	QuotaProjectID string
	ConfigPath     string
	ConfigName     string
}

func resolveFilePath(path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return ""
	}

	// Support "~" and "~/" for user-entered paths (common in UI).
	// We intentionally do not support "~user" expansion.
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~\\") {
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			if p == "~" {
				p = home
			} else {
				// Trim "~/" or "~\".
				p = filepath.Join(home, p[2:])
			}
		}
	}

	// filepath.Clean("") returns ".", so only clean non-empty strings.
	p = filepath.Clean(p)
	return p
}

func credentialsFilePathInfo() credentialsPathInfo {
	if v := strings.TrimSpace(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")); v != "" {
		return credentialsPathInfo{Path: resolveFilePath(v), Source: "google_application_credentials"}
	}
	if v := strings.TrimSpace(os.Getenv("CLOUDSDK_CONFIG")); v != "" {
		v = resolveFilePath(v)
		return credentialsPathInfo{Path: filepath.Join(v, "application_default_credentials.json"), Source: "cloudsdk_config"}
	}

	base := cloudSDKConfigDir()
	if base == "" {
		return credentialsPathInfo{Path: "", Source: "none"}
	}
	return credentialsPathInfo{Path: filepath.Join(base, "application_default_credentials.json"), Source: "default"}
}

func cloudSDKConfigDir() string {
	if v := strings.TrimSpace(os.Getenv("CLOUDSDK_CONFIG")); v != "" {
		return resolveFilePath(v)
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	if runtime.GOOS == "windows" {
		base := strings.TrimSpace(os.Getenv("APPDATA"))
		if base == "" {
			// Best-effort fallback for environments that don't set APPDATA.
			if cfg, err := os.UserConfigDir(); err == nil && strings.TrimSpace(cfg) != "" {
				base = cfg
			} else {
				base = filepath.Join(home, "AppData", "Roaming")
			}
		}
		return filepath.Join(base, "gcloud")
	}
	return filepath.Join(home, ".config", "gcloud")
}

func credentialsFilePath() string {
	return credentialsFilePathInfo().Path
}

func detectedProjectFromCredentials(c credentialsFile) detectedProjectInfo {
	if pid := strings.TrimSpace(c.ProjectID); pid != "" {
		return detectedProjectInfo{
			ProjectID:      pid,
			ProjectSource:  "adc.project_id",
			QuotaProjectID: strings.TrimSpace(c.QuotaProjectID),
		}
	}
	if pid := strings.TrimSpace(c.QuotaProjectID); pid != "" {
		return detectedProjectInfo{
			ProjectID:      pid,
			ProjectSource:  "adc.quota_project_id",
			QuotaProjectID: pid,
		}
	}
	return detectedProjectInfo{QuotaProjectID: strings.TrimSpace(c.QuotaProjectID)}
}

func detectedProjectFromCloudSDKConfig() detectedProjectInfo {
	base := cloudSDKConfigDir()
	if base == "" {
		return detectedProjectInfo{}
	}
	name := "default"
	activePath := filepath.Join(base, "active_config")
	if b, err := os.ReadFile(activePath); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			name = v
		}
	}
	configPath := filepath.Join(base, "configurations", "config_"+name)
	b, err := os.ReadFile(configPath)
	if err != nil {
		if name != "default" {
			configPath = filepath.Join(base, "configurations", "config_default")
			b, err = os.ReadFile(configPath)
		}
		if err != nil {
			return detectedProjectInfo{}
		}
	}
	if pid := projectIDFromCloudSDKConfigBytes(b); pid != "" {
		return detectedProjectInfo{
			ProjectID:     pid,
			ProjectSource: "gcloud.active_config",
			ConfigPath:    configPath,
			ConfigName:    name,
		}
	}
	return detectedProjectInfo{ConfigPath: configPath, ConfigName: name}
}

func projectIDFromCloudSDKConfigBytes(b []byte) string {
	inCore := false
	for _, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.Contains(line, "]") {
			section := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			inCore = strings.EqualFold(section, "core")
			continue
		}
		if !inCore {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "project") {
			continue
		}
		return strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return ""
}

func accessTokenFromCredentialsFile(ctx context.Context, httpc *http.Client, scopes []string) (string, time.Time, error) {
	p := credentialsFilePath()
	return accessTokenFromCredentialsFileAtPath(ctx, httpc, scopes, p)
}

func accessTokenFromCredentialsFileAtPath(ctx context.Context, httpc *http.Client, scopes []string, path string) (string, time.Time, error) {
	p := resolveFilePath(path)
	if p == "" {
		return "", time.Time{}, errors.New("no credentials path configured")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", time.Time{}, err
	}

	var c credentialsFile
	if err := json.Unmarshal(b, &c); err != nil {
		return "", time.Time{}, err
	}

	switch strings.TrimSpace(c.Type) {
	case "authorized_user":
		return tokenFromAuthorizedUser(ctx, httpc, c)
	case "service_account":
		return tokenFromServiceAccount(ctx, httpc, c, scopes)
	default:
		return "", time.Time{}, fmt.Errorf("unsupported credentials type: %s", strings.TrimSpace(c.Type))
	}
}

func tokenFromAuthorizedUser(ctx context.Context, httpc *http.Client, c credentialsFile) (string, time.Time, error) {
	if strings.TrimSpace(c.ClientID) == "" || strings.TrimSpace(c.ClientSecret) == "" || strings.TrimSpace(c.RefreshToken) == "" {
		return "", time.Time{}, errors.New("authorized_user credentials missing client_id, client_secret, or refresh_token")
	}

	form := url.Values{}
	form.Set("client_id", strings.TrimSpace(c.ClientID))
	form.Set("client_secret", strings.TrimSpace(c.ClientSecret))
	form.Set("refresh_token", strings.TrimSpace(c.RefreshToken))
	form.Set("grant_type", "refresh_token")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpc.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var tr oauthTokenResponse
	_ = json.Unmarshal(b, &tr)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(tr.ErrorDescription)
		if msg == "" {
			msg = strings.TrimSpace(string(b))
		}
		if msg == "" {
			msg = fmt.Sprintf("token endpoint error (HTTP %d)", resp.StatusCode)
		}
		return "", time.Time{}, errors.New(msg)
	}

	tok := strings.TrimSpace(tr.AccessToken)
	if tok == "" {
		return "", time.Time{}, errors.New("empty token from token endpoint")
	}
	exp := time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	return tok, exp, nil
}

func tokenFromServiceAccount(ctx context.Context, httpc *http.Client, c credentialsFile, scopes []string) (string, time.Time, error) {
	email := strings.TrimSpace(c.ClientEmail)
	if email == "" {
		return "", time.Time{}, errors.New("service_account credentials missing client_email")
	}
	if strings.TrimSpace(c.PrivateKey) == "" {
		return "", time.Time{}, errors.New("service_account credentials missing private_key")
	}

	tokenURI := strings.TrimSpace(c.TokenURI)
	if tokenURI == "" {
		tokenURI = "https://oauth2.googleapis.com/token"
	}

	priv, err := parseRSAPrivateKeyFromPEM([]byte(c.PrivateKey))
	if err != nil {
		return "", time.Time{}, err
	}

	now := time.Now()
	header := map[string]any{
		"alg": "RS256",
		"typ": "JWT",
	}
	if strings.TrimSpace(c.PrivateKeyID) != "" {
		header["kid"] = strings.TrimSpace(c.PrivateKeyID)
	}
	payload := map[string]any{
		"iss":   email,
		"scope": strings.Join(scopes, " "),
		"aud":   tokenURI,
		"iat":   now.Unix(),
		// Maximum is typically 1h; keep a little margin.
		"exp": now.Add(55 * time.Minute).Unix(),
	}

	hb, err := json.Marshal(header)
	if err != nil {
		return "", time.Time{}, err
	}
	pb, err := json.Marshal(payload)
	if err != nil {
		return "", time.Time{}, err
	}

	signingInput := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(pb)
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, sum[:])
	if err != nil {
		return "", time.Time{}, err
	}
	jwt := signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)

	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	form.Set("assertion", jwt)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpc.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var tr oauthTokenResponse
	_ = json.Unmarshal(b, &tr)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(tr.ErrorDescription)
		if msg == "" {
			msg = strings.TrimSpace(string(b))
		}
		if msg == "" {
			msg = fmt.Sprintf("token endpoint error (HTTP %d)", resp.StatusCode)
		}
		return "", time.Time{}, errors.New(msg)
	}

	tok := strings.TrimSpace(tr.AccessToken)
	if tok == "" {
		return "", time.Time{}, errors.New("empty token from token endpoint")
	}
	exp := time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	return tok, exp, nil
}

func parseRSAPrivateKeyFromPEM(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("invalid private key pem")
	}
	// Service account JSON typically uses PKCS8.
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("private key is not RSA")
		}
		return rk, nil
	}
	// Fallback: PKCS1.
	if rk, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return rk, nil
	}
	return nil, errors.New("failed to parse RSA private key")
}

func accessTokenFromGcloud(ctx context.Context) (string, time.Time, error) {
	if _, ok := localexec.ResolveCommand("gcloud"); !ok {
		return "", time.Time{}, errors.New("gcloud executable was not found")
	}
	cmd := localexec.CommandContext(ctx, "gcloud", "auth", "application-default", "print-access-token")
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", time.Time{}, errors.New("gcloud token error: " + msg)
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" {
		return "", time.Time{}, errors.New("empty token from gcloud")
	}
	// gcloud doesn't tell us expiry. Cache conservatively.
	return tok, time.Now().Add(50 * time.Minute), nil
}
