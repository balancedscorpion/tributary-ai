package githubcli

import (
	"errors"
	"regexp"
	"strings"
)

const redactedGitHubToken = "[REDACTED_GITHUB_TOKEN]"

var githubTokenRe = regexp.MustCompile(`\b(?:gh[a-z]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,})\b`)

func redactText(text string, exactTokens ...string) string {
	if text == "" {
		return ""
	}
	out := text
	for _, token := range exactTokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		out = strings.ReplaceAll(out, token, redactedGitHubToken)
	}
	return githubTokenRe.ReplaceAllString(out, redactedGitHubToken)
}

func redactError(err error, exactTokens ...string) error {
	if err == nil {
		return nil
	}
	return errors.New(redactText(err.Error(), exactTokens...))
}

func redactRunResult(res RunResult, exactTokens ...string) RunResult {
	res.Stdout = redactText(res.Stdout, exactTokens...)
	res.Stderr = redactText(res.Stderr, exactTokens...)
	return res
}
