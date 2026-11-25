package api

import (
	"bytes"
	"fmt"
	"io"
	"strings"
)

func writeSSE(w io.Writer, eventType string, data []byte) error {
	if strings.TrimSpace(eventType) != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", eventType); err != nil {
			return err
		}
	}
	// SSE supports multiple data: lines, but our payload should already be a single-line JSON.
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if _, err := fmt.Fprintf(w, "data: %s\n", line); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "\n")
	return err
}

func writeSSEComment(w io.Writer, comment string) error {
	comment = strings.ReplaceAll(comment, "\n", " ")
	_, err := fmt.Fprintf(w, ": %s\n\n", comment)
	return err
}
