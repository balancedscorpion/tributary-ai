package optionc

import "strings"

// Image generation is billable, so run-scoped connection selection is
// enforced again at execution time. Tool definitions are advisory to model
// providers and cannot be the only authorization boundary.
func isImageGenerationToolAllowedForRun(toolName string, enabled bool) bool {
	if strings.TrimSpace(toolName) != "generate_image" {
		return true
	}
	return enabled
}
