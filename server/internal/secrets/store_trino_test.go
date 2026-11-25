package secrets

import "testing"

func TestClearTrinoByAuthType(t *testing.T) {
	t.Run("clear basic preserves jwt", func(t *testing.T) {
		store := newUnlockedSecretsStore(t)
		if err := store.SetTrinoJWT("jwt-token"); err != nil {
			t.Fatalf("SetTrinoJWT: %v", err)
		}
		if err := store.ClearTrino("basic"); err != nil {
			t.Fatalf("ClearTrino(basic): %v", err)
		}
		sec, err := store.GetTrino()
		if err != nil {
			t.Fatalf("GetTrino: %v", err)
		}
		if sec.Mode != "jwt" {
			t.Fatalf("Mode = %q, want jwt", sec.Mode)
		}
		if sec.JWT != "jwt-token" {
			t.Fatalf("JWT = %q, want jwt-token", sec.JWT)
		}
	})

	t.Run("clear jwt preserves basic", func(t *testing.T) {
		store := newUnlockedSecretsStore(t)
		if err := store.SetTrinoBasicAuth("alice", "secret"); err != nil {
			t.Fatalf("SetTrinoBasicAuth: %v", err)
		}
		if err := store.ClearTrino("jwt"); err != nil {
			t.Fatalf("ClearTrino(jwt): %v", err)
		}
		sec, err := store.GetTrino()
		if err != nil {
			t.Fatalf("GetTrino: %v", err)
		}
		if sec.Mode != "basic" {
			t.Fatalf("Mode = %q, want basic", sec.Mode)
		}
		if sec.Username != "alice" || sec.Password != "secret" {
			t.Fatalf("basic credentials changed unexpectedly: username=%q password=%q", sec.Username, sec.Password)
		}
	})

	t.Run("clear all removes trino auth", func(t *testing.T) {
		store := newUnlockedSecretsStore(t)
		if err := store.SetTrinoBasicAuth("alice", "secret"); err != nil {
			t.Fatalf("SetTrinoBasicAuth: %v", err)
		}
		if err := store.ClearTrino("all"); err != nil {
			t.Fatalf("ClearTrino(all): %v", err)
		}
		sec, err := store.GetTrino()
		if err != nil {
			t.Fatalf("GetTrino: %v", err)
		}
		if sec.Mode != "none" {
			t.Fatalf("Mode = %q, want none", sec.Mode)
		}
		if sec.Username != "" || sec.Password != "" || sec.JWT != "" {
			t.Fatalf("expected empty credentials after clear all")
		}
	})

	t.Run("unknown auth type fails", func(t *testing.T) {
		store := newUnlockedSecretsStore(t)
		if err := store.ClearTrino("sso"); err == nil {
			t.Fatalf("expected unknown auth type to fail")
		}
	})
}

func newUnlockedSecretsStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Init("passphrase"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Unlock("passphrase"); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	return store
}
