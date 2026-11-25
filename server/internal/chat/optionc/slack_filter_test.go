package optionc

import (
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/config"
)

func TestSlackEnablementFromSettings(t *testing.T) {
	settings := config.DefaultSettings()
	if slackEnabledFromSettings(&settings) {
		t.Fatal("Slack should default to disabled")
	}

	enabled := true
	readTools := true
	settings.Slack.Enabled = &enabled
	settings.Slack.ReadTools = nil
	if !slackEnabledFromSettings(&settings) {
		t.Fatal("Slack should be enabled when enabled is true and readTools is unset")
	}

	readTools = false
	settings.Slack.ReadTools = &readTools
	if slackEnabledFromSettings(&settings) {
		t.Fatal("Slack should be disabled when readTools is explicitly false")
	}

	readTools = true
	settings.Slack.ReadTools = &readTools
	if !slackEnabledFromSettings(&settings) {
		t.Fatal("Slack should be enabled when enabled and readTools are true")
	}

	writeTools := true
	settings.Slack.ReadTools = &readTools
	settings.Slack.WriteTools = &writeTools
	access := slackToolAccessFromSettings(&settings)
	if !access.Read || !access.Write {
		t.Fatalf("Slack access = %#v, want read and write", access)
	}
}

func TestSlackToolGate(t *testing.T) {
	disabled := slackToolAccess{}
	readOnly := slackToolAccess{Read: true}
	writeOnly := slackToolAccess{Write: true}
	all := slackToolAccess{Read: true, Write: true}
	if !isSlackToolAllowedForRun("read_file", disabled) {
		t.Fatal("non-Slack tools should not be blocked")
	}
	for _, name := range []string{
		"slack_status",
		"slack_check_connection",
		"slack_list_conversations",
		"slack_get_conversation",
		"slack_get_conversation_members",
		"slack_get_messages",
		"slack_get_thread_replies",
		"slack_get_user",
		"slack_search_messages",
	} {
		if isSlackToolAllowedForRun(name, disabled) {
			t.Fatalf("%s should be blocked when Slack is disabled", name)
		}
		if !isSlackToolAllowedForRun(name, readOnly) {
			t.Fatalf("%s should be allowed when Slack read tools are enabled", name)
		}
		if isSlackToolAllowedForRun(name, writeOnly) {
			t.Fatalf("%s should be blocked when only Slack write tools are enabled", name)
		}
	}
	for _, name := range []string{"slack_post_message", "slack_upload_file"} {
		if isSlackToolAllowedForRun(name, disabled) {
			t.Fatalf("%s should be blocked when Slack is disabled", name)
		}
		if isSlackToolAllowedForRun(name, readOnly) {
			t.Fatalf("%s should be blocked when only Slack read tools are enabled", name)
		}
		if !isSlackToolAllowedForRun(name, writeOnly) || !isSlackToolAllowedForRun(name, all) {
			t.Fatalf("%s should be allowed when Slack write tools are enabled", name)
		}
	}
}

func TestFilterToolsBySlackEnabled(t *testing.T) {
	defs := []types.ToolDefinition{
		{Function: types.ToolFunctionDefinition{Name: "read_file"}},
		{Function: types.ToolFunctionDefinition{Name: "slack_status"}},
		{Function: types.ToolFunctionDefinition{Name: "slack_post_message"}},
	}

	disabled := filterToolsBySlackEnabled(defs, slackToolAccess{})
	if len(disabled) != 1 || disabled[0].Function.Name != "read_file" {
		t.Fatalf("disabled filter = %#v, want only read_file", disabled)
	}

	readOnly := filterToolsBySlackEnabled(defs, slackToolAccess{Read: true})
	if len(readOnly) != 2 {
		t.Fatalf("read filter length = %d, want 2", len(readOnly))
	}

	writeOnly := filterToolsBySlackEnabled(defs, slackToolAccess{Write: true})
	if len(writeOnly) != 2 || writeOnly[1].Function.Name != "slack_post_message" {
		t.Fatalf("write filter = %#v, want read_file and slack_post_message", writeOnly)
	}
}

func TestEmbedSlackGuidanceOnlyWhenEnabled(t *testing.T) {
	base := "base prompt"
	if got := embedSlackGuidance(base, slackToolAccess{}); got != base {
		t.Fatalf("disabled guidance = %q, want base prompt", got)
	}
	got := embedSlackGuidance(base, slackToolAccess{Read: true, Write: true})
	for _, needle := range []string{
		"Slack tools:",
		"only queue approvals",
		"untrusted workspace data",
		"Never request, reveal, paste, or expose Slack OAuth tokens",
		"next_cursor",
		"slack_post_message",
		"allowed Slack workspace/channel settings",
	} {
		if !strings.Contains(got, needle) {
			t.Fatalf("enabled guidance missing %q: %q", needle, got)
		}
	}
}
