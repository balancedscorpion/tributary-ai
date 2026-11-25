package activation

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ai-analyst-local/internal/atomicfile"
)

const (
	defaultTokenFile      = "activation.token"
	defaultDeviceSeedFile = "device.v1.seed"
)

type Options struct {
	PublicKey ed25519.PublicKey

	// DevBypass disables activation enforcement (Activated() always true).
	// Intended for local development outside Docker.
	DevBypass bool
}

type Status struct {
	KeyInstalled        bool           `json:"key_installed"`
	Activated           bool           `json:"activated"`
	DeviceID            string         `json:"device_id"`
	DeviceIDSource      string         `json:"device_id_source,omitempty"`
	ExpiresAt           *time.Time     `json:"expires_at,omitempty"`
	PublicKeyConfigured bool           `json:"public_key_configured"`
	Error               string         `json:"error,omitempty"`
	Claims              map[string]any `json:"claims,omitempty"`
}

type Store struct {
	mu sync.Mutex

	path           string
	publicKey      ed25519.PublicKey
	deviceID       string
	deviceIDSource string
	lastErr        string
	devBypass      bool

	keyInstalled bool
	activated    bool
	expiresAt    *time.Time
	claims       map[string]any
	token        string
}

func NewStore(configDir string, opts Options) (*Store, error) {
	if strings.TrimSpace(configDir) == "" {
		return nil, errors.New("config dir is required")
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return nil, err
	}

	pub := opts.PublicKey
	if len(pub) == 0 && len(DefaultPublicKey) == ed25519.PublicKeySize {
		pub = DefaultPublicKey
	}

	st := &Store{
		path:      filepath.Join(configDir, defaultTokenFile),
		publicKey: pub,
		devBypass: opts.DevBypass,
	}
	st.deviceID, st.deviceIDSource, st.lastErr = readDeviceID(configDir)
	if st.lastErr != "" {
		// Keep going; this will surface in status and gate operations.
		st.lastErr = "device id: " + st.lastErr
	}

	if b, err := os.ReadFile(st.path); err == nil {
		st.token = strings.TrimSpace(string(b))
		if st.token != "" {
			st.keyInstalled = true
			st.refreshLocked()
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	return st, nil
}

func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.token = ""
	s.keyInstalled = false
	s.activated = false
	s.expiresAt = nil
	s.claims = nil
	s.lastErr = "activation key not installed"

	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *Store) Install(token string) (Status, error) {
	clean := strings.TrimSpace(token)
	if clean == "" {
		return s.Status(), errors.New("token is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.token = clean
	s.keyInstalled = true
	if err := atomicfile.WriteFile(s.path, []byte(clean+"\n"), 0o600); err != nil {
		s.lastErr = err.Error()
		s.activated = false
		return s.statusLocked(), err
	}
	s.refreshLocked()
	if !s.activated {
		return s.statusLocked(), errors.New(s.lastErr)
	}
	return s.statusLocked(), nil
}

func (s *Store) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.refreshLocked()
	return s.statusLocked()
}

func (s *Store) Activated() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.refreshLocked()
	return s.activated
}

func (s *Store) statusLocked() Status {
	return Status{
		KeyInstalled:        s.keyInstalled,
		Activated:           s.activated,
		DeviceID:            s.deviceID,
		DeviceIDSource:      s.deviceIDSource,
		ExpiresAt:           s.expiresAt,
		PublicKeyConfigured: len(s.publicKey) == ed25519.PublicKeySize,
		Error:               s.lastErr,
		Claims:              s.claims,
	}
}

func (s *Store) refreshLocked() {
	if s.devBypass {
		s.keyInstalled = true
		s.activated = true
		s.expiresAt = nil
		s.claims = map[string]any{"dev_bypass": true}
		s.lastErr = ""
		return
	}
	if strings.TrimSpace(s.deviceID) == "" {
		s.activated = false
		s.expiresAt = nil
		s.claims = nil
		if strings.TrimSpace(s.lastErr) != "" {
			// Preserve the error captured during initialization (e.g. config seed unavailable).
			return
		}
		s.lastErr = "device id unavailable"
		return
	}
	if s.keyInstalled == false || strings.TrimSpace(s.token) == "" {
		s.activated = false
		s.expiresAt = nil
		s.claims = nil
		s.lastErr = "activation key not installed"
		return
	}
	if len(s.publicKey) != ed25519.PublicKeySize {
		s.activated = false
		s.expiresAt = nil
		s.claims = nil
		s.lastErr = "activation public key not configured"
		return
	}
	claims, exp, err := verifyToken(s.token, s.deviceID, s.publicKey, time.Now())
	if err != nil {
		s.activated = false
		s.expiresAt = exp
		s.claims = claims
		s.lastErr = err.Error()
		return
	}
	if exp != nil && exp.Before(time.Now()) {
		s.activated = false
		s.expiresAt = exp
		s.claims = claims
		s.lastErr = "activation expired"
		return
	}
	if exp != nil {
		s.expiresAt = exp
	}
	s.claims = claims
	s.activated = true
	s.lastErr = ""
}

func readDeviceID(configDir string) (deviceID string, source string, errStr string) {
	// Device ID is always derived from a random seed persisted in the config dir.
	// This is cross-platform (Windows/Mac/Linux) and stable in Docker when the config
	// directory is mounted as a persistent volume.
	seed, err := loadOrCreateDeviceSeed(configDir)
	if err != nil {
		return "", "", err.Error()
	}
	sum := sha256.Sum256(seed)
	return hex.EncodeToString(sum[:]), "config_seed", ""
}

func loadOrCreateDeviceSeed(configDir string) ([]byte, error) {
	p := filepath.Join(configDir, defaultDeviceSeedFile)
	if b, err := os.ReadFile(p); err == nil {
		trimmed := strings.TrimSpace(string(b))
		if trimmed == "" {
			return nil, errors.New("device seed file is empty")
		}
		decoded, err := base64.StdEncoding.DecodeString(trimmed)
		if err != nil {
			decoded, err = base64.RawStdEncoding.DecodeString(trimmed)
		}
		if err != nil {
			return nil, errors.New("device seed file is invalid base64")
		}
		if len(decoded) < 16 {
			return nil, errors.New("device seed file is too short")
		}
		return decoded, nil
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	seed := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, seed); err != nil {
		return nil, err
	}
	encoded := base64.StdEncoding.EncodeToString(seed)
	if err := atomicfile.WriteFile(p, []byte(encoded+"\n"), 0o600); err != nil {
		return nil, err
	}
	return seed, nil
}

func verifyToken(token string, deviceID string, publicKey ed25519.PublicKey, now time.Time) (map[string]any, *time.Time, error) {
	payload, sig, claims, err := parseToken(token)
	if err != nil {
		return claims, nil, err
	}
	if !ed25519.Verify(publicKey, payload, sig) {
		return claims, nil, errors.New("invalid activation signature")
	}
	claimedFP, ok := claimString(claims, "device_id")
	if !ok {
		claimedFP, ok = claimString(claims, "fingerprint")
	}
	if !ok {
		if alt, okAlt := claimString(claims, "machine_id"); okAlt {
			claimedFP = alt
			ok = true
		}
	}
	if !ok || strings.TrimSpace(claimedFP) == "" {
		return claims, nil, errors.New("activation missing device id")
	}
	if strings.TrimSpace(claimedFP) != strings.TrimSpace(deviceID) {
		return claims, nil, errors.New("activation device id mismatch")
	}
	if exp, ok := claimExpiry(claims); ok {
		if exp.Before(now) {
			return claims, &exp, errors.New("activation expired")
		}
		return claims, &exp, nil
	}
	return claims, nil, nil
}

func parseToken(token string) ([]byte, []byte, map[string]any, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 2 {
		return nil, nil, nil, errors.New("invalid activation key format")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, nil, nil, errors.New("invalid activation payload encoding")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, nil, nil, errors.New("invalid activation signature encoding")
	}
	if len(sig) != ed25519.SignatureSize {
		return nil, nil, nil, errors.New("invalid activation signature size")
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, nil, nil, errors.New("invalid activation payload json")
	}
	return payload, sig, claims, nil
}

func claimString(claims map[string]any, key string) (string, bool) {
	if claims == nil {
		return "", false
	}
	v, ok := claims[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	return s, true
}

func claimExpiry(claims map[string]any) (time.Time, bool) {
	if claims == nil {
		return time.Time{}, false
	}
	if v, ok := claims["exp"]; ok {
		switch tv := v.(type) {
		case float64:
			return time.Unix(int64(tv), 0).UTC(), true
		case int64:
			return time.Unix(tv, 0).UTC(), true
		case json.Number:
			if i, err := tv.Int64(); err == nil {
				return time.Unix(i, 0).UTC(), true
			}
		}
	}
	if v, ok := claims["expires_at"]; ok {
		if s, ok := v.(string); ok {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				return t.UTC(), true
			}
		}
	}
	return time.Time{}, false
}
