package auditcap_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/auditcap"
)

// t0 is the start of a minute; the cap's windows are whole minutes.
var t0 = time.Date(2026, 10, 10, 3, 0, 0, 0, time.UTC)

func TestOneRowPerReasonAndPeerPerMinuteThenTheCarriedCount(t *testing.T) {
	t.Parallel()
	c := auditcap.New()
	due, row := c.Note(t0, "ENROLL_TOKEN_USED", "192.0.2.1")
	if due != nil || row == nil || row.Suppressed != 0 || row.Reason != "ENROLL_TOKEN_USED" || row.Peer != "192.0.2.1" {
		t.Fatalf("first refusal: due %+v row %+v, want a row with nothing suppressed", due, row)
	}
	for i := range 3 {
		if due, row := c.Note(t0.Add(time.Duration(10+i)*time.Second), "ENROLL_TOKEN_USED", "192.0.2.1"); due != nil || row != nil {
			t.Fatalf("repeat %d in the same minute: due %+v row %+v, want suppressed", i, due, row)
		}
	}
	// Another reason from the same peer is another pair.
	if _, row := c.Note(t0.Add(20*time.Second), "ENROLL_TOKEN_EXPIRED", "192.0.2.1"); row == nil {
		t.Fatal("another reason from the same peer was suppressed")
	}
	due, row = c.Note(t0.Add(70*time.Second), "ENROLL_TOKEN_USED", "192.0.2.1")
	want := auditcap.Summary{Window: t0, Suppressed: 3, DistinctPeers: 1, Reasons: map[string]int{"ENROLL_TOKEN_USED": 3}}
	if due == nil || !equalSummary(*due, want) {
		t.Errorf("next minute's summary = %+v, want %+v", due, want)
	}
	if row == nil || row.Suppressed != 3 {
		t.Errorf("next minute's row = %+v, want suppressed=3", row)
	}
	if _, row := c.Note(t0.Add(3*time.Minute), "ENROLL_TOKEN_USED", "192.0.2.1"); row == nil || row.Suppressed != 0 {
		t.Errorf("a later row = %+v, want suppressed=0 (the count was carried once)", row)
	}
}

func equalSummary(a, b auditcap.Summary) bool {
	return fmt.Sprint(a) == fmt.Sprint(b)
}

func TestTenRowsAMinuteInAllThenASummary(t *testing.T) {
	t.Parallel()
	c := auditcap.New()
	rows := 0
	for i := range 25 {
		reason := "ENROLL_TOKEN_UNKNOWN"
		if i%2 == 1 {
			reason = "ENROLL_CSR_INVALID"
		}
		if due, row := c.Note(t0.Add(time.Duration(i)*time.Second), reason, fmt.Sprintf("192.0.2.%d", i)); row != nil {
			rows++
		} else if due != nil {
			t.Fatalf("a summary inside its own minute: %+v", due)
		}
	}
	if rows != 10 {
		t.Fatalf("%d rows in one minute, want 10", rows)
	}
	due, row := c.Note(t0.Add(time.Minute), "ENROLL_TOKEN_UNKNOWN", "192.0.2.12")
	// Peers 10..24 were suppressed: 8 even (UNKNOWN), 7 odd (CSR).
	want := auditcap.Summary{
		Window: t0, Suppressed: 15, DistinctPeers: 15,
		Reasons: map[string]int{"ENROLL_CSR_INVALID": 7, "ENROLL_TOKEN_UNKNOWN": 8},
	}
	if due == nil || !equalSummary(*due, want) {
		t.Errorf("summary = %+v, want %+v", due, want)
	}
	if row == nil || row.Suppressed != 1 {
		t.Errorf("peer 12's next row = %+v, want suppressed=1", row)
	}
}

// A flood over several minutes: per window at most 10 rows and one summary,
// and every refusal is in a row or a summary once Flush runs.
func TestAFloodIsCappedAndFullyCounted(t *testing.T) {
	t.Parallel()
	c := auditcap.New()
	const minutes, perMinute = 5, 1000
	reasons := []string{"ENROLL_TOKEN_UNKNOWN", "ENROLL_TOKEN_USED", "ENROLL_TOKEN_EXPIRED"}
	rowsIn := map[time.Time]int{}
	summaries := map[time.Time]int{}
	counted := 0
	take := func(due *auditcap.Summary) {
		if due != nil {
			summaries[due.Window]++
			counted += due.Suppressed
		}
	}
	for m := range minutes {
		for i := range perMinute {
			now := t0.Add(time.Duration(m)*time.Minute + time.Duration(i)*50*time.Millisecond)
			due, row := c.Note(now, reasons[i%3], fmt.Sprintf("198.51.100.%d", i%200))
			take(due)
			if row != nil {
				rowsIn[now.Truncate(time.Minute)]++
				counted++
			}
		}
	}
	take(c.Flush())
	for w, n := range rowsIn {
		if n > 10 {
			t.Errorf("window %s: %d rows, want at most 10", w.Format(time.TimeOnly), n)
		}
	}
	for w, n := range summaries {
		if n != 1 {
			t.Errorf("window %s: %d summaries, want 1", w.Format(time.TimeOnly), n)
		}
	}
	if len(summaries) != minutes {
		t.Errorf("%d summaries, want one per minute (%d)", len(summaries), minutes)
	}
	if counted != minutes*perMinute {
		t.Errorf("rows plus suppressed = %d, want every refusal (%d)", counted, minutes*perMinute)
	}
}

func TestFlushTakesTheMinuteInProgressOnce(t *testing.T) {
	t.Parallel()
	c := auditcap.New()
	if s := c.Flush(); s != nil {
		t.Errorf("Flush with nothing suppressed = %+v", s)
	}
	c.Note(t0, "ENROLL_TOKEN_USED", "192.0.2.1")
	if s := c.Flush(); s != nil {
		t.Errorf("Flush after one written row = %+v, want nil", s)
	}
	c.Note(t0.Add(time.Second), "ENROLL_TOKEN_USED", "192.0.2.1")
	s := c.Flush()
	if s == nil || s.Suppressed != 1 || !s.Window.Equal(t0) {
		t.Errorf("Flush = %+v, want the minute in progress with 1 suppressed", s)
	}
	if s := c.Flush(); s != nil {
		t.Errorf("second Flush = %+v, want nil", s)
	}
	if due, _ := c.Note(t0.Add(time.Minute), "ENROLL_TOKEN_USED", "192.0.2.1"); due != nil {
		t.Errorf("the flushed minute came back as %+v", due)
	}
}

// Distinct peers are counted up to MaxTracked, then the summary says so.
func TestTrackingIsBounded(t *testing.T) {
	t.Parallel()
	c := auditcap.New()
	n := auditcap.MaxTracked + 500
	for i := range n {
		c.Note(t0, "ENROLL_TOKEN_UNKNOWN", fmt.Sprintf("2001:db8::%x", i))
	}
	s := c.Flush()
	if s == nil || s.Suppressed != n-10 || s.DistinctPeers != auditcap.MaxTracked || !s.PeersCapped {
		t.Errorf("summary = %+v, want %d suppressed, %d peers, capped", s, n-10, auditcap.MaxTracked)
	}
}

// A clock that steps back never reopens an earlier minute.
func TestAClockStepBackStaysInTheCurrentMinute(t *testing.T) {
	t.Parallel()
	c := auditcap.New()
	c.Note(t0.Add(time.Minute), "ENROLL_TOKEN_USED", "192.0.2.1")
	if due, row := c.Note(t0, "ENROLL_TOKEN_USED", "192.0.2.1"); due != nil || row != nil {
		t.Errorf("after a step back: due %+v row %+v, want suppressed in the current minute", due, row)
	}
}

func TestConcurrentNotesAreAllCounted(t *testing.T) {
	t.Parallel()
	c := auditcap.New()
	var mu sync.Mutex
	counted := 0
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 200 {
				due, row := c.Note(t0.Add(time.Duration(i)*time.Second), "ENROLL_TOKEN_UNKNOWN", fmt.Sprintf("192.0.2.%d", g))
				mu.Lock()
				if due != nil {
					counted += due.Suppressed
				}
				if row != nil {
					counted++
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if s := c.Flush(); s != nil {
		counted += s.Suppressed
	}
	if counted != 8*200 {
		t.Errorf("counted %d, want %d", counted, 8*200)
	}
}
