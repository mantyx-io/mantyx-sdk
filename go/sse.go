package mantyx

import (
	"bufio"
	"io"
	"strings"
)

// SseEvent is one Server-Sent Events frame.
type SseEvent struct {
	ID    string
	Event string
	Data  string
}

// readSseStream parses a `text/event-stream` response body. It blocks on the
// reader and emits a frame whenever it sees a blank-line separator. Comment
// frames (lines starting with ":") are skipped — they're typically heartbeat
// pings.
func readSseStream(body io.Reader, onEvent func(SseEvent) bool) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 16*1024), 8*1024*1024)

	var (
		id        string
		eventType string
		dataLines []string
	)

	flush := func() bool {
		if len(dataLines) == 0 && id == "" && eventType == "" {
			return true
		}
		ev := SseEvent{ID: id, Event: eventType, Data: strings.Join(dataLines, "\n")}
		id = ""
		eventType = ""
		dataLines = dataLines[:0]
		return onEvent(ev)
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if !flush() {
				return nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // comment / heartbeat
		}
		field, value := splitField(line)
		switch field {
		case "id":
			id = value
		case "event":
			eventType = value
		case "data":
			dataLines = append(dataLines, value)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	flush()
	return nil
}

func splitField(line string) (string, string) {
	idx := strings.IndexByte(line, ':')
	if idx == -1 {
		return line, ""
	}
	field := line[:idx]
	value := line[idx+1:]
	if strings.HasPrefix(value, " ") {
		value = value[1:]
	}
	return field, value
}
