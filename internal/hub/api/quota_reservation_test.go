package api

import (
	"context"
	"io"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/linuxdynasty/fleet-board/internal/hub/config"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

func TestQuotaReservationRefundIsTerminal(t *testing.T) {
	tel, err := telemetry.New(telemetry.Config{
		Service: telemetry.Hub, Version: "test", InstanceID: "test", LogOutput: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	q, err := NewQuotas(config.RateLimits{Read: 1, Write: 1, Unauthenticated: 1, Metrics: 1}, tel,
		func() time.Time { return time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	first, err := q.reserveWrite(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	first.refund()
	first.refund()
	second, err := q.reserveWrite(t.Context())
	if err != nil {
		t.Fatalf("one refund must free one allowance: %v", err)
	}
	first.refund()
	second.consume()
	second.refund()
	if _, err := q.reserveWrite(t.Context()); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("duplicate refund or refund after consume over-credited budget: %v", err)
	}
}
