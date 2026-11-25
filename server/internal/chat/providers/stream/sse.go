package stream

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
)

type Message struct {
	Event string
	Data  string
	Err   error
}

const (
	// Guardrails for pathological provider streams.
	// Values are intentionally high to avoid truncating normal LLM traffic.
	maxSSELineBytes  = 4 * 1024 * 1024
	maxSSEEventBytes = 16 * 1024 * 1024
)

// Parse reads a text/event-stream response body and emits parsed messages.
// It is tolerant of partial line reads and supports multi-line data blocks.
func Parse(ctx context.Context, r io.Reader) <-chan Message {
	out := make(chan Message, 16)
	go func() {
		defer close(out)

		br := bufio.NewReaderSize(r, 64*1024)
		var (
			eventName string
			dataLines []string
			eventSize int
		)

		reset := func() {
			eventName = ""
			dataLines = dataLines[:0]
			eventSize = 0
		}

		emit := func(msg Message) bool {
			select {
			case out <- msg:
				return true
			case <-ctx.Done():
				return false
			}
		}

		emitErr := func(err error) bool {
			if err == nil {
				return true
			}
			return emit(Message{Err: err})
		}

		flush := func() bool {
			if len(dataLines) == 0 {
				eventName = ""
				return true
			}
			msg := Message{Data: strings.Join(dataLines, "\n")}
			if strings.TrimSpace(eventName) != "" {
				msg.Event = eventName
			}
			reset()
			return emit(msg)
		}

		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			line, err := br.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					// Pragmatic EOF flush: some providers close without a trailing blank line.
					line = strings.TrimRight(line, "\r\n")
					if line != "" {
						if len(line) > maxSSELineBytes {
							_ = emitErr(fmt.Errorf("sse line exceeded %d bytes", maxSSELineBytes))
							return
						}
						if parseErr := processLine(line, &eventName, &dataLines, &eventSize, maxSSEEventBytes); parseErr != nil {
							_ = emitErr(parseErr)
							return
						}
					}
					_ = flush()
					return
				}
				_ = emitErr(fmt.Errorf("sse read error: %w", err))
				return
			}

			line = strings.TrimRight(line, "\r\n")
			if len(line) > maxSSELineBytes {
				_ = emitErr(fmt.Errorf("sse line exceeded %d bytes", maxSSELineBytes))
				return
			}
			if line == "" {
				if ok := flush(); !ok {
					return
				}
				continue
			}
			if parseErr := processLine(line, &eventName, &dataLines, &eventSize, maxSSEEventBytes); parseErr != nil {
				_ = emitErr(parseErr)
				return
			}
		}
	}()
	return out
}

func processLine(
	line string,
	eventName *string,
	dataLines *[]string,
	eventSize *int,
	maxEventBytes int,
) error {
	// Comments
	if strings.HasPrefix(line, ":") {
		return nil
	}

	fieldName := line
	fieldValue := ""
	if idx := strings.Index(line, ":"); idx >= 0 {
		fieldName = line[:idx]
		fieldValue = line[idx+1:]
		fieldValue = strings.TrimPrefix(fieldValue, " ")
	}

	if fieldName == "event" {
		*eventName = fieldValue
		return nil
	}
	if fieldName != "data" {
		return nil
	}

	nextSize := *eventSize + len(fieldValue)
	if len(*dataLines) > 0 {
		nextSize += 1
	}
	if nextSize > maxEventBytes {
		return fmt.Errorf("sse event exceeded %d bytes", maxEventBytes)
	}

	*eventSize = nextSize
	*dataLines = append(*dataLines, fieldValue)
	return nil
}
