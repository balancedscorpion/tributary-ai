package sharepoint

import (
	"strings"
	"testing"
)

func TestEmbeddedBridgeDoesNotExposeCredentialFields(t *testing.T) {
	script := string(embeddedBridgeScript)
	for _, forbidden := range []string{
		"Authorization",
		"Cookie",
		"FedAuth",
		"rtFa",
		"SPA_RT",
		"storageState",
	} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("embedded bridge script contains credential-bearing term %q", forbidden)
		}
	}
}

func TestBridgeConfigDefaultsProfileInsideConfigDir(t *testing.T) {
	cfgDir := t.TempDir()
	manager := NewManager(cfgDir, nil)
	got := manager.bridgeConfig()
	if !pathWithinRoot(got.BrowserProfileDir, cfgDir) {
		t.Fatalf("default profile dir %q should be inside cfgDir %q", got.BrowserProfileDir, cfgDir)
	}
	if got.AuthMode != "browser" {
		t.Fatalf("AuthMode=%q, want browser", got.AuthMode)
	}
	if !got.BrowserHeadless {
		t.Fatalf("BrowserHeadless=false, want true by default")
	}
}
