package statusfile

import "encoding/json"

// encode renders a file's content: indented, so the owner can read it with
// cat, and ending in a newline.
func encode(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
