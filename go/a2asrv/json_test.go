package a2asrv_test

import (
	"encoding/json"
	"io"
)

// Tiny JSON helpers shared by the test stub. Pulled out of the main test
// file for readability.

func decodeJSON(r io.Reader, v any) error {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	return dec.Decode(v)
}

func encodeJSON(w io.Writer, v any) error {
	return json.NewEncoder(w).Encode(v)
}
