package optionc

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
)

var httpStatusCodeRe = regexp.MustCompile(`\b([1-5][0-9]{2})\b`)

func enrichToolErrorOutput(toolName string, call types.ToolCall, out tools.Output) tools.Output {
	if out.OK {
		return out
	}
	result := toolErrorResultMap(out)
	message := firstStringMapValue(result, "message", "error", "description", "detail")
	if message == "" {
		message = toolErrorMessageFromOutput(out)
	}
	if message == "" {
		message = "unknown error"
	}
	result["error"] = message
	if _, exists := result["message"]; !exists {
		result["message"] = message
	}

	resultMetadata := ensureMutableMap(result["result_metadata"])
	diagnostic := ensureMutableMap(resultMetadata["diagnostic"])
	if len(diagnostic) == 0 {
		if existing := ensureMutableMap(result["diagnostic"]); len(existing) > 0 {
			diagnostic = existing
		}
	}

	fillToolErrorDiagnostic(diagnostic, toolName, call, result, message)
	resultMetadata["diagnostic"] = diagnostic
	resultMetadata["model_commentary_required"] = true
	resultMetadata["model_commentary_instruction"] = "Explain this tool failure to the user with the likely cause and the reported error before finishing the response."
	result["result_metadata"] = resultMetadata

	out.ResultFormat = tools.ResultFormatJSON
	out.Result = result
	return out
}

func toolErrorResultMap(out tools.Output) map[string]any {
	if mm, ok := out.Result.(map[string]any); ok && mm != nil {
		cp := make(map[string]any, len(mm)+2)
		for key, value := range mm {
			cp[key] = value
		}
		return cp
	}
	message := toolErrorMessageFromOutput(out)
	result := map[string]any{}
	if message != "" {
		result["error"] = message
	}
	if out.Result != nil {
		result["raw_result"] = out.Result
	}
	return result
}

func toolErrorMessageFromOutput(out tools.Output) string {
	switch v := out.Result.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		if v == nil {
			return ""
		}
		if b, err := json.Marshal(v); err == nil {
			return strings.TrimSpace(string(b))
		}
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}

func ensureMutableMap(value any) map[string]any {
	if mm, ok := value.(map[string]any); ok && mm != nil {
		cp := make(map[string]any, len(mm))
		for key, nested := range mm {
			cp[key] = nested
		}
		return cp
	}
	return map[string]any{}
}

func fillToolErrorDiagnostic(diagnostic map[string]any, toolName string, call types.ToolCall, result map[string]any, message string) {
	kind, title, why, tryThis := classifyToolError(toolName, message)
	code := firstStringMapValue(result, "code", "status_code", "http_status", "error_code", "status")
	if code == "" {
		code = firstHTTPStatusCode(message)
	}

	putStringDefault(diagnostic, "title", title)
	putStringDefault(diagnostic, "kind", kind)
	putStringDefault(diagnostic, "message", message)
	putStringDefault(diagnostic, "why", why)
	putStringDefault(diagnostic, "try_this", tryThis)
	putStringDefault(diagnostic, "reported", message)
	if code != "" {
		putStringDefault(diagnostic, "code", code)
	}
	if failedRequest := toolFailedRequestLine(toolName, call, result); failedRequest != "" {
		putStringDefault(diagnostic, "failed_request", failedRequest)
	}
}

func putStringDefault(mm map[string]any, key string, value string) {
	if mm == nil || strings.TrimSpace(value) == "" {
		return
	}
	if existing := strings.TrimSpace(fmt.Sprint(mm[key])); existing != "" && existing != "<nil>" {
		return
	}
	mm[key] = strings.TrimSpace(value)
}

func classifyToolError(toolName string, message string) (kind string, title string, why string, tryThis string) {
	lower := strings.ToLower(strings.TrimSpace(message + " " + toolName))
	switch {
	case strings.Contains(lower, "401") || strings.Contains(lower, "unauthorized") || strings.Contains(lower, "unauthenticated") || strings.Contains(lower, "bad credentials") || strings.Contains(lower, "invalid token") || strings.Contains(lower, "missing token"):
		return "auth", "Authentication required", "The tool's connection credentials were missing, expired, or rejected by the remote service.", "Reconnect or reconfigure the integration, then run the request again."
	case strings.Contains(lower, "403") || strings.Contains(lower, "forbidden") || strings.Contains(lower, "permission denied") || strings.Contains(lower, "access denied") || strings.Contains(lower, "not authorized"):
		return "permission", "Permission denied", "The connected account authenticated, but it does not have permission for this resource or operation.", "Verify the account, scopes, repository, project, dataset, or workspace permissions."
	case strings.Contains(lower, "404") || strings.Contains(lower, "not found") || strings.Contains(lower, "does not exist") || strings.Contains(lower, "no such file") || strings.Contains(lower, "not visible"):
		return "notfound", "Not found", "The requested resource could not be found, or it is not visible to the connected account.", "Check the resource name/path and confirm the connected account has access."
	case strings.Contains(lower, "429") || strings.Contains(lower, "rate limit") || strings.Contains(lower, "too many requests") || strings.Contains(lower, "quota"):
		return "ratelimit", "Rate limited", "The service temporarily rejected the request because a quota or rate limit was reached.", "Wait briefly, reduce request volume, or retry with a narrower request."
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "timed out") || strings.Contains(lower, "deadline exceeded"):
		return "timeout", "Timed out", "The tool did not receive a response before its timeout.", "Retry the request or narrow the operation so it completes faster."
	case strings.Contains(lower, "400") || strings.Contains(lower, "invalid json") || strings.Contains(lower, "invalid argument") || strings.Contains(lower, "missing ") || strings.Contains(lower, "required") || strings.Contains(lower, "syntax") || strings.Contains(lower, "validation"):
		return "validation", "Invalid request", "The tool rejected the request because one or more arguments or query fields were invalid.", "Correct the request arguments and run the tool again."
	case strings.Contains(lower, "500") || strings.Contains(lower, "502") || strings.Contains(lower, "503") || strings.Contains(lower, "504") || strings.Contains(lower, "unavailable") || strings.Contains(lower, "connection refused") || strings.Contains(lower, "failed to fetch") || strings.Contains(lower, "network"):
		return "unavailable", "Service unavailable", "The service or network path failed before the tool could complete.", "Retry after the service recovers, or check the connector/network configuration."
	default:
		return "generic", "Tool request failed", "The tool returned an error before completing the requested operation.", "Review the reported error and adjust the request or connection before retrying."
	}
}

func firstHTTPStatusCode(message string) string {
	match := httpStatusCodeRe.FindStringSubmatch(message)
	if len(match) >= 2 {
		return match[1]
	}
	return ""
}

func toolFailedRequestLine(toolName string, call types.ToolCall, result map[string]any) string {
	if command := firstStringMapValue(result, "failed_request", "command", "request", "endpoint"); command != "" {
		return command
	}
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		toolName = strings.TrimSpace(call.Name)
	}
	if toolName == "" {
		return ""
	}
	argsText := strings.TrimSpace(call.ArgsText)
	if argsText == "" && call.Args != nil {
		if b, err := json.Marshal(call.Args); err == nil {
			argsText = string(b)
		}
	}
	if argsText == "" {
		argsText = "{}"
	}
	return fmt.Sprintf("%s(%s)", toolName, argsText)
}
