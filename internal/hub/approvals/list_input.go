package approvals

import (
	"fmt"
	"time"
)

type listInput struct {
	size   int
	want   string
	cursor *pageCursor
	state  string
	kind   string
}

// ValidateListInput checks a List request without reading approval rows.
// It uses List's exact page defaults, cursor binding and filter checks, so
// callers can put a quota checkpoint before the first database effect.
func ValidateListInput(f Filter, now time.Time) error {
	_, err := parseListInput(f, now)
	return err
}

func parseListInput(f Filter, now time.Time) (listInput, error) {
	in := listInput{size: f.PageSize}
	if in.size <= 0 {
		in.size = 50
	}
	if in.size > 500 {
		in.size = 500
	}
	if now.IsZero() {
		return listInput{}, fmt.Errorf("%w: now is required", ErrInvalid)
	}
	in.want = queryHash(f, in.size)
	if f.PageToken != "" {
		var err error
		in.cursor, err = parseToken(f.PageToken, in.want, now)
		if err != nil {
			return listInput{}, err
		}
	}
	if f.HasState {
		in.state = stateName(f.State)
		if in.state == "" {
			return listInput{}, fmt.Errorf("%w: bad state filter", ErrInvalid)
		}
	}
	if f.HasKind {
		in.kind = kindName(f.Kind)
		if in.kind == "" {
			return listInput{}, fmt.Errorf("%w: bad kind filter", ErrInvalid)
		}
	}
	return in, nil
}
