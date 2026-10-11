package doctor

import (
	"encoding/json"
	"fmt"
	"io"
)

// WriteText prints the report for a person: the two folders used, one line
// per check (status, title, reason), a fix line under each check that has
// one, and a count of each status.
func WriteText(w io.Writer, r Report) error {
	p := &printer{w: w}
	p.printf("fleet-agent doctor\n")
	p.printf("state folder:  %s\n", r.StateDir)
	p.printf("config folder: %s\n\n", r.ConfigDir)
	for _, c := range r.Checks {
		p.printf("%-13s %s: %s\n", "["+string(c.Status)+"]", c.Title, c.Reason)
		if c.Fix != "" {
			p.printf("%-13s fix: %s\n", "", c.Fix)
		}
	}
	s := r.Summary
	p.printf("\n%d ok, %d warn, %d fail, %d unknown, %d not_checked\n", s.OK, s.Warn, s.Fail, s.Unknown, s.NotChecked)
	return p.err
}

// WriteJSON prints the report as one JSON object (`doctor --json`).
func WriteJSON(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// printer remembers the first write error.
type printer struct {
	w   io.Writer
	err error
}

func (p *printer) printf(format string, a ...any) {
	if p.err == nil {
		_, p.err = fmt.Fprintf(p.w, format, a...)
	}
}
