package messagepaging

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	cursorVersion = 1
	maxTokenBytes = 64 << 10
)

// Cursor is the complete, self-contained continuation state for a
// list_messages request. Keeping the filters in the token means callers only
// need to copy next_page_token; they do not need to reconstruct a sequence
// window or repeat every filter accurately.
type Cursor struct {
	Version       int      `json:"v"`
	ThreadID      string   `json:"t"`
	OrderBy       string   `json:"o"`
	BeforeSeq     *int64   `json:"b,omitempty"`
	AfterSeq      *int64   `json:"a,omitempty"`
	BeforeTS      string   `json:"bt,omitempty"`
	AfterTS       string   `json:"at,omitempty"`
	MessageIDs    []string `json:"m,omitempty"`
	ArtifactIDs   []string `json:"g,omitempty"`
	Limit         int      `json:"l"`
	MaxChars      int      `json:"c"`
	IncludeHidden bool     `json:"h,omitempty"`
}

func Encode(cursor Cursor) (string, error) {
	cursor.Version = cursorVersion
	if err := Validate(cursor); err != nil {
		return "", err
	}
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	if len(payload) > maxTokenBytes {
		return "", errors.New("message page token state is too large")
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func Decode(raw string) (Cursor, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Cursor{}, errors.New("page_token is required")
	}
	if len(raw) > base64.RawURLEncoding.EncodedLen(maxTokenBytes) {
		return Cursor{}, errors.New("page_token is too large")
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return Cursor{}, errors.New("page_token is invalid")
	}
	if len(payload) > maxTokenBytes {
		return Cursor{}, errors.New("page_token is too large")
	}
	var cursor Cursor
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return Cursor{}, errors.New("page_token is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Cursor{}, errors.New("page_token is invalid")
	}
	if err := Validate(cursor); err != nil {
		return Cursor{}, fmt.Errorf("page_token is invalid: %w", err)
	}
	return cursor, nil
}

func Validate(cursor Cursor) error {
	if cursor.Version != cursorVersion {
		return errors.New("unsupported cursor version")
	}
	if strings.TrimSpace(cursor.ThreadID) == "" {
		return errors.New("thread_id is missing")
	}
	switch cursor.OrderBy {
	case "seq_desc", "seq_asc":
	default:
		return errors.New("order_by is invalid")
	}
	if cursor.BeforeSeq != nil && *cursor.BeforeSeq < 0 {
		return errors.New("before_seq is invalid")
	}
	if cursor.AfterSeq != nil && *cursor.AfterSeq < 0 {
		return errors.New("after_seq is invalid")
	}
	if cursor.BeforeSeq != nil && cursor.AfterSeq != nil && *cursor.BeforeSeq <= *cursor.AfterSeq {
		return errors.New("sequence window is empty")
	}
	if cursor.Limit < 1 || cursor.Limit > 100 {
		return errors.New("limit is invalid")
	}
	if cursor.MaxChars < 1 || cursor.MaxChars > 3000 {
		return errors.New("max_chars_per_result is invalid")
	}
	if len(cursor.MessageIDs) > 100 || len(cursor.ArtifactIDs) > 100 {
		return errors.New("filter list is too large")
	}
	for _, values := range [][]string{cursor.MessageIDs, cursor.ArtifactIDs} {
		for _, value := range values {
			if strings.TrimSpace(value) == "" {
				return errors.New("filter contains an empty id")
			}
		}
	}
	beforeTS, err := parseOptionalTime(cursor.BeforeTS)
	if err != nil {
		return errors.New("before_ts is invalid")
	}
	afterTS, err := parseOptionalTime(cursor.AfterTS)
	if err != nil {
		return errors.New("after_ts is invalid")
	}
	if beforeTS != nil && afterTS != nil && beforeTS.Before(*afterTS) {
		return errors.New("timestamp window is empty")
	}
	return nil
}

// Advance returns the cursor for the page after a page whose last returned
// message has lastSeq. Sequence bounds are exclusive, so using that exact
// sequence as the next boundary prevents gaps and duplicates.
func Advance(cursor Cursor, lastSeq int64) Cursor {
	next := clone(cursor)
	if next.OrderBy == "seq_asc" {
		next.AfterSeq = int64Ptr(lastSeq)
	} else {
		next.BeforeSeq = int64Ptr(lastSeq)
	}
	return next
}

func clone(cursor Cursor) Cursor {
	out := cursor
	if cursor.BeforeSeq != nil {
		out.BeforeSeq = int64Ptr(*cursor.BeforeSeq)
	}
	if cursor.AfterSeq != nil {
		out.AfterSeq = int64Ptr(*cursor.AfterSeq)
	}
	out.MessageIDs = append([]string(nil), cursor.MessageIDs...)
	out.ArtifactIDs = append([]string(nil), cursor.ArtifactIDs...)
	return out
}

func int64Ptr(value int64) *int64 {
	return &value
}

func parseOptionalTime(raw string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
