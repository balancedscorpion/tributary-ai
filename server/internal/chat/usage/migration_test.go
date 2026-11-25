package usage

import (
	"os"
	"strings"
	"testing"
)

func TestModelInvocationsMigrationHasLedgerCountersAndIndexes(t *testing.T) {
	legacy, err := os.ReadFile("../../db/migrate/migrations/0015_model_invocations.sql")
	if err != nil {
		t.Fatalf("read immutable legacy migration: %v", err)
	}
	contents, err := os.ReadFile("../../db/migrate/migrations/0016_model_invocations.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sqlText := strings.ToLower(string(contents))
	fragments := []string{
		"alter table model_invocations",
		"provider_total_tokens bigint null",
		"derived_total_tokens bigint null",
		"change column provider_config_id connection_id varchar(200) character set utf8mb4 collate utf8mb4_bin null",
		"change column api_type planned_api_surface varchar(64) character set utf8mb4 collate utf8mb4_bin null",
		"plan_id char(36) character set utf8mb4 collate utf8mb4_bin null",
		"config_fingerprint varchar(128) character set utf8mb4 collate utf8mb4_bin null",
		"effective_api_surface varchar(64) character set utf8mb4 collate utf8mb4_bin null",
		"snapshot_sequence bigint unsigned not null default 0",
		"lower_bound_mask bigint unsigned not null default 0",
		"token_semantics_profile varchar(32)",
		"set provider_config_id = null",
		"where profile_id is not null and trim(profile_id) <> ''",
		"idx_model_invocations_connection (connection_id, ended_at)",
		"idx_model_invocations_plan (plan_id)",
		"idx_model_invocations_running_updated (status, updated_at)",
		"drop column total_tokens",
		"drop column error_message",
		"drop column provider_config_id",
		"effective_api_surface = case when status = 'completed'",
		"where status <> 'completed' or usage_quality = 'legacy_partial'",
		"provider_id = 'bedrock' and lower(coalesce(api_type, '')) in ('responses', 'response') then 'responses'",
		"provider_id = 'bedrock' and lower(coalesce(api_type, '')) in ('chat_completions', 'chat_completion') then 'chat_completions'",
		"provider_id = 'bedrock' and lower(coalesce(api_type, '')) like '%converse%' then 'converse_stream'",
		"when provider_id = 'bedrock' then 'invoke_stream'",
	}
	for _, fragment := range fragments {
		if !strings.Contains(sqlText, fragment) {
			t.Errorf("migration is missing %q", fragment)
		}
	}
	combined := strings.ToLower(string(legacy)) + "\n" + sqlText
	if !strings.Contains(combined, "create table if not exists model_invocations") ||
		!strings.Contains(combined, "modify column input_tokens bigint null") ||
		!strings.Contains(combined, "modify column output_tokens bigint null") ||
		!strings.Contains(combined, "modify column total_tokens bigint null") {
		t.Error("immutable 0015 plus 0016 must support fresh installs and preserve BIGINT run rollups")
	}
	if strings.Contains(combined, "foreign key (thread_id)") || strings.Contains(combined, "foreign key (run_id)") {
		t.Error("usage ledger must survive thread/run deletion; attribution columns should not cascade")
	}
	if strings.Contains(sqlText, "create table if not exists model_invocations") {
		t.Error("0016 must transform the immutable 0015 schema instead of hiding it behind CREATE IF NOT EXISTS")
	}
}
