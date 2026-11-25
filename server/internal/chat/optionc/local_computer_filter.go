package optionc

import "strings"

func isLocalComputerToolAllowedForRun(toolName string, enabled bool) bool {
	switch strings.TrimSpace(toolName) {
	case "file_search", "local_list_folder", "local_read_file", "local_open_file", "local_read_pdf_page", "local_list_grant_folder", "local_search_command", "local_search_in_grant", "local_read_grant_file":
		return enabled
	default:
		return true
	}
}
