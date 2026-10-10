package outbox_test

import (
	"context"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/scrub"
)

// The secrets seeded into agent free text (row 5 ruling, amendment 8).
const (
	seededToken = "ghp_" + "0123456789abcdefghijABCDEFGHIJ012345"
	seededEmail = "al@example.com"
	seededQuery = "code=" + "q9secretquery"
	seededPass  = "hunter2pass"
)

var seeded = "push failed: token " + seededToken + " for " + seededEmail +
	" at https://hub.example/cb?" + seededQuery + " via https://al:" + seededPass + "@db.example.com/x"

// The hub scrubs CommandResult.message and CommandProgress.message before
// it stores them: a seeded token, email, URL query value and password in
// a URL are gone from the row, the stored result and the progress message,
// and what is stored passes the scrubber unchanged (idempotent). The
// outcomes are counted per field.
func TestStoredFreeTextIsScrubbed(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	h := newService(t, s, newFakeStreams(), fixedClock(t0))
	mustEnqueue(t, s, "m1", "c1")
	mustEnqueue(t, s, "m1", "c2")
	ctx := context.Background()
	if _, err := h.svc.Progress(ctx, live1, &agentv1.CommandProgress{CommandId: cid("c2"), Step: "s", Message: seeded}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Result(ctx, live1, resultOf("c1", typev1.Status_STATUS_FAILED, seeded)); err != nil {
		t.Fatal(err)
	}
	r1, _ := readRow(t, s, "c1")
	r2, _ := readRow(t, s, "c2")
	for name, text := range map[string]string{
		"result row": r1.message, "stored result": storedResult(t, s, "c1").GetMessage(), "progress row": r2.message,
	} {
		for _, secret := range []string{seededToken, seededEmail, "q9secretquery", seededPass} {
			if strings.Contains(text, secret) {
				t.Errorf("%s keeps %q: %q", name, secret, text)
			}
		}
		if !strings.Contains(text, "push failed") || !strings.Contains(text, "db.example.com") {
			t.Errorf("%s lost the text around the secrets: %q", name, text)
		}
		if again := scrub.Text(text, scrub.Options{MaxBytes: outbox.MaxMessage}); again != text {
			t.Errorf("%s is not stable under a second pass: %q -> %q", name, text, again)
		}
	}
	body := h.scrape(t)
	for _, field := range []string{"command_result.message", "command_progress.message"} {
		if v := sample(body, "fleet_hub_scrub_total", `field="`+field+`"`, `result="redacted"`); v != 1 {
			t.Errorf("fleet_hub_scrub_total{%s, redacted} = %v, want 1", field, v)
		}
	}
}

// A hostile message far over the 1 KiB cap is cut to the cap before the
// scrub: it is stored at most 1 KiB, counted as oversize, and costs what a
// 1 KiB message costs. Unbounded, the scrubber takes about 16 s on 1 MiB
// of this shape (04b); the bound here is far above the cost of 1 KiB.
func TestAnOversizeMessageIsCutBeforeTheScrub(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	h := newService(t, s, newFakeStreams(), fixedClock(t0))
	mustEnqueue(t, s, "m1", "c1")
	hostile := strings.Repeat("a=b&", (1<<20-4096)/4)
	start := time.Now()
	if _, err := h.svc.Result(context.Background(), live1, resultOf("c1", typev1.Status_STATUS_FAILED, hostile)); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("a 1 MiB message took %v to apply", took)
	}
	r, _ := readRow(t, s, "c1")
	if len(r.message) == 0 || len(r.message) > outbox.MaxMessage {
		t.Errorf("stored %d bytes, want 1 to %d", len(r.message), outbox.MaxMessage)
	}
	if v := sample(h.scrape(t), "fleet_hub_scrub_oversize_total", `field="command_result.message"`); v != 1 {
		t.Errorf("fleet_hub_scrub_oversize_total = %v, want 1", v)
	}
}
