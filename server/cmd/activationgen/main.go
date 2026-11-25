package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const defaultExpiresInDays = 180

type claimFlag []string

func (c *claimFlag) String() string { return strings.Join(*c, ",") }

func (c *claimFlag) Set(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("claim cannot be empty")
	}
	*c = append(*c, value)
	return nil
}

func main() {
	var privateKeyPath string
	var fingerprint string
	var expiresInDays int
	var outPath string
	var activationID string
	var issuedTo string
	var product string
	var issuedAtStr string
	var claims claimFlag
	var printPublicKeyGo bool

	flag.StringVar(&privateKeyPath, "private-key", "", "path to ed25519 private key (PEM PKCS8 or base64); use - to read from stdin")
	flag.StringVar(&fingerprint, "fingerprint", "", "device fingerprint (paste from /api/activation/status device_id)")
	flag.IntVar(&expiresInDays, "expires-in-days", defaultExpiresInDays, "number of days until expiration")
	flag.StringVar(&outPath, "out", "", "output path for activation key (default stdout)")
	flag.StringVar(&activationID, "activation-id", "", "activation identifier (default random UUID)")
	flag.StringVar(&issuedTo, "issued-to", "", "who this activation is issued to (optional)")
	flag.StringVar(&product, "product", "ai-analyst-local", "product identifier (optional)")
	flag.StringVar(&issuedAtStr, "issued-at", "", "issue time RFC3339 (default now)")
	flag.Var(&claims, "claim", "custom claim in key=value form (repeatable)")
	flag.BoolVar(&printPublicKeyGo, "print-public-key-go", false, "print a Go snippet for DefaultPublicKey to stderr")
	flag.Parse()

	if strings.TrimSpace(privateKeyPath) == "" {
		fail("missing --private-key")
	}
	if strings.TrimSpace(fingerprint) == "" {
		fail("missing --fingerprint")
	}
	if expiresInDays <= 0 {
		fail("--expires-in-days must be > 0")
	}

	privKey, pubKey, err := loadEd25519PrivateKey(privateKeyPath)
	if err != nil {
		fail("private key: %v", err)
	}
	if printPublicKeyGo {
		printGoPublicKeySnippet(os.Stderr, pubKey)
	}

	issuedAt, err := parseIssuedAt(issuedAtStr)
	if err != nil {
		fail("issued-at: %v", err)
	}
	expiresAt := issuedAt.Add(time.Duration(expiresInDays) * 24 * time.Hour)

	if strings.TrimSpace(activationID) == "" {
		activationID, err = newUUID()
		if err != nil {
			fail("activation-id: %v", err)
		}
	}

	claimMap, err := parseClaimsMap(claims)
	if err != nil {
		fail("claim: %v", err)
	}

	payload := map[string]any{
		"v":          1,
		"activation_id": strings.TrimSpace(activationID),
		"product":    strings.TrimSpace(product),
		"issued_to":  strings.TrimSpace(issuedTo),
		"device_id":  strings.TrimSpace(fingerprint),
		"iat":        issuedAt.UTC().Unix(),
		"exp":        expiresAt.UTC().Unix(),
	}
	if payload["issued_to"] == "" {
		delete(payload, "issued_to")
	}
	if len(claimMap) > 0 {
		payload["claims"] = claimMap
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		fail("payload: %v", err)
	}
	sig := ed25519.Sign(privKey, payloadJSON)

	token := base64.RawURLEncoding.EncodeToString(payloadJSON) + "." + base64.RawURLEncoding.EncodeToString(sig)
	token = token + "\n"

	if strings.TrimSpace(outPath) == "" {
		if _, err := os.Stdout.WriteString(token); err != nil {
			fail("write stdout: %v", err)
		}
		return
	}
	if err := os.WriteFile(outPath, []byte(token), 0o600); err != nil {
		fail("write %s: %v", outPath, err)
	}
}

func parseIssuedAt(input string) (time.Time, error) {
	if strings.TrimSpace(input) == "" {
		return time.Now().UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(input))
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

func parseClaimsMap(entries []string) (map[string]string, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for _, entry := range entries {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("expected key=value, got %q", entry)
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "" {
			return nil, fmt.Errorf("empty claim key in %q", entry)
		}
		out[key] = value
	}
	return out, nil
}

func loadEd25519PrivateKey(path string) (ed25519.PrivateKey, ed25519.PublicKey, error) {
	data, err := readKeyData(path)
	if err != nil {
		return nil, nil, err
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil, errors.New("empty private key data")
	}

	if block, _ := pem.Decode(data); block != nil {
		return parsePEM(block)
	}

	if key, pub, err := parseBase64Key(data); err == nil {
		return key, pub, nil
	}

	return nil, nil, errors.New("unsupported private key format (expected PEM PKCS8 or base64 ed25519 key)")
}

func readKeyData(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

func parsePEM(block *pem.Block) (ed25519.PrivateKey, ed25519.PublicKey, error) {
	if block.Type != "PRIVATE KEY" && block.Type != "ED25519 PRIVATE KEY" {
		return nil, nil, fmt.Errorf("unsupported PEM type %q", block.Type)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, err
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, nil, errors.New("PEM is not an ed25519 private key")
	}
	return key, key.Public().(ed25519.PublicKey), nil
}

func parseBase64Key(data []byte) (ed25519.PrivateKey, ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(string(data))
		if err != nil {
			return nil, nil, err
		}
	}
	switch len(raw) {
	case ed25519.PrivateKeySize:
		key := ed25519.PrivateKey(raw)
		return key, key.Public().(ed25519.PublicKey), nil
	case ed25519.SeedSize:
		key := ed25519.NewKeyFromSeed(raw)
		return key, key.Public().(ed25519.PublicKey), nil
	default:
		return nil, nil, fmt.Errorf("unexpected key length %d", len(raw))
	}
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	hexStr := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", hexStr[0:8], hexStr[8:12], hexStr[12:16], hexStr[16:20], hexStr[20:32]), nil
}

func fail(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(os.Stderr, "activationgen:", msg)
	os.Exit(1)
}

func printGoPublicKeySnippet(w io.Writer, pub ed25519.PublicKey) {
	if len(pub) != ed25519.PublicKeySize {
		fmt.Fprintln(w, "activationgen: public key unavailable or wrong size")
		return
	}
	fmt.Fprintln(w, "/* Paste into server/internal/activation/publickey.go */")
	fmt.Fprintln(w, "var DefaultPublicKey ed25519.PublicKey = ed25519.PublicKey{")
	for i, b := range pub {
		sep := ","
		if i == len(pub)-1 {
			sep = ","
		}
		fmt.Fprintf(w, "  0x%02x%s\n", b, sep)
	}
	fmt.Fprintln(w, "}")
}
