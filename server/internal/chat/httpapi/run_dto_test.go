package httpapi

import (
	"testing"

	"ai-analyst-local/internal/chat/store"
)

func TestRunToDTOUsesCanonicalConnectionAndLegacyProfileIdentities(t *testing.T) {
	profileID := "legacy-profile"
	modern := runToDTO(store.Run{ConnectionID: "connection-a"})
	if modern["connection_id"] != "connection-a" {
		t.Fatalf("modern run DTO = %#v", modern)
	}
	if _, exists := modern["provider_config_id"]; exists {
		t.Fatalf("modern run DTO exposed removed provider_config_id alias: %#v", modern)
	}
	legacy := runToDTO(store.Run{ProfileID: &profileID})
	if legacy["profile_id"] != profileID {
		t.Fatalf("legacy run DTO = %#v", legacy)
	}
	if _, exists := legacy["connection_id"]; exists {
		t.Fatalf("legacy profile DTO fabricated a connection identity: %#v", legacy)
	}
}
