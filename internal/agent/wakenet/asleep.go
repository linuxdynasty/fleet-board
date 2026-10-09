package wakenet

import (
	"errors"
	"fmt"
	"time"
)

const (
	// bracketWindow is the most the awake clock may move across one bracketed
	// read before the read is tried again: a longer window means the thread was
	// descheduled between the reads and the difference is not trustworthy.
	bracketWindow = 10 * time.Millisecond
	bracketTries  = 3
)

var errClocksApart = errors.New("clock reads too far apart")

// asleepFrom returns the time asleep since boot: the clock that counts sleep
// minus the clock that does not. The awake clock is read before and after the
// sleeping one and the midpoint is used, so the error is at most half the
// window instead of the whole delay between two reads.
func asleepFrom(read func(id int32) (time.Duration, error), withSleep, awake int32) (time.Duration, error) {
	for range bracketTries {
		a1, err := read(awake)
		if err != nil {
			return 0, fmt.Errorf("read awake clock %d: %w", awake, err)
		}
		s, err := read(withSleep)
		if err != nil {
			return 0, fmt.Errorf("read sleep-counting clock %d: %w", withSleep, err)
		}
		a2, err := read(awake)
		if err != nil {
			return 0, fmt.Errorf("read awake clock %d: %w", awake, err)
		}
		if a2-a1 > bracketWindow || a2 < a1 {
			continue
		}
		return s - (a1 + (a2-a1)/2), nil
	}
	return 0, fmt.Errorf("%w: %d tries over %v", errClocksApart, bracketTries, bracketWindow)
}
