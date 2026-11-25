package stream

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

type chunkReader struct {
	b     []byte
	pos   int
	chunk int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.b) {
		return 0, io.EOF
	}
	n := r.chunk
	if n <= 0 {
		n = 1
	}
	if n > len(p) {
		n = len(p)
	}
	remain := len(r.b) - r.pos
	if n > remain {
		n = remain
	}
	copy(p[:n], r.b[r.pos:r.pos+n])
	r.pos += n
	return n, nil
}

func TestParse_BasicAndMultiline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	input := strings.Join([]string{
		": comment",
		"event: foo",
		"data: line1",
		"data: line2",
		"",
		"data: {\"ok\":true}",
	}, "\n")

	var got []Message
	for m := range Parse(ctx, strings.NewReader(input)) {
		got = append(got, m)
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d: %#v", len(got), got)
	}
	if got[0].Event != "foo" {
		t.Fatalf("expected event foo, got %q", got[0].Event)
	}
	if got[0].Data != "line1\nline2" {
		t.Fatalf("unexpected data: %q", got[0].Data)
	}
	if got[1].Event != "" {
		t.Fatalf("expected empty event, got %q", got[1].Event)
	}
	if got[1].Data != "{\"ok\":true}" {
		t.Fatalf("unexpected data: %q", got[1].Data)
	}
}

func TestParse_LongLine_NoScannerLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	big := strings.Repeat("a", 200_000)
	input := "data: " + big + "\n\n"

	var got []Message
	for m := range Parse(ctx, strings.NewReader(input)) {
		got = append(got, m)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got))
	}
	if got[0].Data != big {
		t.Fatalf("unexpected data length: got %d want %d", len(got[0].Data), len(big))
	}
}

func TestParse_ChunkedReader(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	input := "data: one\n\ndata: two\n\n"
	r := &chunkReader{b: []byte(input), chunk: 3}

	var got []Message
	for m := range Parse(ctx, r) {
		got = append(got, m)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	if got[0].Data != "one" || got[1].Data != "two" {
		t.Fatalf("unexpected messages: %#v", got)
	}
}

func TestParse_FlushesIncompleteTrailingEventAtEOF(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	input := "data: trailing"
	var got []Message
	for m := range Parse(ctx, strings.NewReader(input)) {
		if m.Err != nil {
			t.Fatalf("unexpected parse error: %v", m.Err)
		}
		got = append(got, m)
	}

	if len(got) != 1 {
		t.Fatalf("expected 1 message, got %#v", got)
	}
	if got[0].Data != "trailing" {
		t.Fatalf("unexpected data: %q", got[0].Data)
	}
}

func TestParse_ReportsOversizedEvent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	big := strings.Repeat("x", maxSSEEventBytes+1)
	input := "data: " + big + "\n\n"

	var gotErr error
	for m := range Parse(ctx, strings.NewReader(input)) {
		if m.Err != nil {
			gotErr = m.Err
			break
		}
	}
	if gotErr == nil {
		t.Fatalf("expected parse error for oversized event")
	}
}
