package messagepaging

import "testing"

func TestCursorRoundTripAndAdvance(t *testing.T) {
	after := int64(2)
	cursor := Cursor{
		ThreadID: "thread-1", OrderBy: "seq_desc", AfterSeq: &after,
		BeforeTS: "2026-07-14T20:00:00Z", MessageIDs: []string{"message-1"},
		Limit: 20, MaxChars: 3000,
	}
	token, err := Encode(cursor)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	decoded, err := Decode(token)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if decoded.ThreadID != cursor.ThreadID || decoded.OrderBy != cursor.OrderBy || decoded.AfterSeq == nil || *decoded.AfterSeq != after || len(decoded.MessageIDs) != 1 {
		t.Fatalf("decoded cursor=%#v", decoded)
	}
	next := Advance(decoded, 10)
	if next.BeforeSeq == nil || *next.BeforeSeq != 10 || next.AfterSeq == nil || *next.AfterSeq != after {
		t.Fatalf("advanced cursor=%#v", next)
	}
	if _, err := Encode(next); err != nil {
		t.Fatalf("Encode(advanced): %v", err)
	}
}

func TestCursorRejectsEmptySequenceWindow(t *testing.T) {
	before := int64(4)
	after := int64(4)
	_, err := Encode(Cursor{
		ThreadID: "thread-1", OrderBy: "seq_desc", BeforeSeq: &before, AfterSeq: &after,
		Limit: 20, MaxChars: 3000,
	})
	if err == nil {
		t.Fatal("Encode accepted an empty sequence window")
	}
}
