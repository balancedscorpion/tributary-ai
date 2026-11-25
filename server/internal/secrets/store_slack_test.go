package secrets

import "testing"

func TestSlackOAuthSecretsTransitions(t *testing.T) {
	store := newUnlockedSecretsStore(t)

	_, _, cfg := store.Status()
	if cfg.Slack {
		t.Fatalf("Slack = true before set, want false")
	}

	if err := store.SetSlackOAuth(SlackSecrets{
		ClientSecret:   "  oauth-client-secret  ",
		AccessToken:    "  oauth-access-token  ",
		RefreshToken:   "  oauth-refresh-token  ",
		Scope:          " channels:read,channels:history ",
		TeamID:         "  T123  ",
		TeamName:       "  Example Workspace  ",
		EnterpriseID:   "  E123  ",
		AuthedUserID:   "  U123  ",
		BotAccessToken: "  xoxb-bot-token  ",
		BotScope:       " chat:write,files:write ",
		BotUserID:      "  B123  ",
		BotID:          "  A123  ",
	}); err != nil {
		t.Fatalf("SetSlackOAuth: %v", err)
	}

	sec, err := store.GetSlack()
	if err != nil {
		t.Fatalf("GetSlack after set: %v", err)
	}
	if sec.AccessToken != "oauth-access-token" {
		t.Fatalf("AccessToken = %q, want trimmed token", sec.AccessToken)
	}
	if sec.ClientSecret != "oauth-client-secret" {
		t.Fatalf("ClientSecret = %q, want trimmed secret", sec.ClientSecret)
	}
	if sec.RefreshToken != "oauth-refresh-token" {
		t.Fatalf("RefreshToken = %q, want trimmed refresh token", sec.RefreshToken)
	}
	if sec.TokenType != "bearer" {
		t.Fatalf("TokenType = %q, want bearer default", sec.TokenType)
	}
	if sec.Scope != "channels:read,channels:history" {
		t.Fatalf("Scope = %q, want trimmed scope", sec.Scope)
	}
	if sec.TeamID != "T123" || sec.TeamName != "Example Workspace" || sec.EnterpriseID != "E123" || sec.AuthedUserID != "U123" {
		t.Fatalf("Slack metadata was not normalized: %#v", sec)
	}
	if sec.BotAccessToken != "xoxb-bot-token" || sec.BotScope != "chat:write,files:write" || sec.BotUserID != "B123" || sec.BotID != "A123" {
		t.Fatalf("Slack bot metadata was not normalized: %#v", sec)
	}
	if sec.InstalledAt <= 0 || sec.LastRefreshed <= 0 {
		t.Fatalf("expected install timestamps to be populated, got InstalledAt=%d LastRefreshed=%d", sec.InstalledAt, sec.LastRefreshed)
	}

	_, _, cfg = store.Status()
	if !cfg.Slack || !cfg.SlackUser || !cfg.SlackBot || cfg.SlackTeamID != "T123" || cfg.SlackTeamName != "Example Workspace" || cfg.SlackScope != "channels:read,channels:history" || cfg.SlackBotScope != "chat:write,files:write" {
		t.Fatalf("unexpected Slack configured status after set: %#v", cfg)
	}
	if !cfg.SlackClientSecret {
		t.Fatal("SlackClientSecret should be true after set")
	}

	store.Lock()
	if err := store.Unlock("passphrase"); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	afterUnlock, err := store.GetSlack()
	if err != nil {
		t.Fatalf("GetSlack after unlock: %v", err)
	}
	if afterUnlock.AccessToken != sec.AccessToken || afterUnlock.TeamID != sec.TeamID || afterUnlock.Scope != sec.Scope {
		t.Fatalf("Slack secret did not round trip through encrypted store: before=%#v after=%#v", sec, afterUnlock)
	}

	if err := store.ClearSlackOAuth(); err != nil {
		t.Fatalf("ClearSlackOAuth: %v", err)
	}
	cleared, err := store.GetSlack()
	if err != nil {
		t.Fatalf("GetSlack after clear: %v", err)
	}
	if cleared.AccessToken != "" || cleared.RefreshToken != "" || cleared.BotAccessToken != "" || cleared.TeamID != "" || cleared.Scope != "" || cleared.BotScope != "" {
		t.Fatalf("Slack secrets should be cleared, got %#v", cleared)
	}
	if cleared.ClientSecret != "oauth-client-secret" {
		t.Fatalf("Slack client secret should be preserved when clearing OAuth connection, got %#v", cleared)
	}
	_, _, cfg = store.Status()
	if cfg.Slack || cfg.SlackUser || cfg.SlackBot || cfg.SlackTeamID != "" || cfg.SlackTeamName != "" || cfg.SlackScope != "" || cfg.SlackBotScope != "" || !cfg.SlackClientSecret {
		t.Fatalf("Slack configured status should be false after clear, got %#v", cfg)
	}

	if err := store.ClearSlackClientSecret(); err != nil {
		t.Fatalf("ClearSlackClientSecret: %v", err)
	}
	withoutClientSecret, err := store.GetSlack()
	if err != nil {
		t.Fatalf("GetSlack after clear client secret: %v", err)
	}
	if withoutClientSecret.ClientSecret != "" {
		t.Fatalf("Slack client secret should be cleared, got %#v", withoutClientSecret)
	}
	_, _, cfg = store.Status()
	if cfg.SlackClientSecret {
		t.Fatalf("SlackClientSecret should be false after clear, got %#v", cfg)
	}
}
