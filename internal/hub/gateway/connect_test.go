package gateway_test

import (
	"context"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

// HelloAck carries what the agent checks (its session.go accept): the
// fleet, proto version 1, epoch max(stored, last_epoch) + 1, hub time,
// the 15 s heartbeat, the 1 MiB cap and the hub addresses, plus the
// settings epoch and a hub instance id; no hub_capabilities. The epoch is
// stored in one live row before the answer.
func TestConnectAnswersHelloWithHelloAck(t *testing.T) {
	t.Parallel()
	f := listenerFixture(t)
	f.id.HubAddresses = []string{"hub.example:7443"}
	h := startHubOn(t, f)
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	ack := s.ack(t)
	if ack.GetFleetId() != "fleet-1" || ack.GetProtoVersion() != 1 || ack.GetEpoch() != 1 ||
		ack.GetMaxMessageBytes() != 1<<20 || ack.GetHeartbeatInterval().AsDuration() != 15*time.Second ||
		ack.GetSettingsEpoch() != "epoch-1" || ack.GetHubInstanceId() == "" || len(ack.GetHubCapabilities()) != 1 ||
		ack.GetHubCapabilities()[0].GetName() != "event.ack" || ack.GetHubCapabilities()[0].GetVersion() != 1 ||
		ack.GetHubLastEventSeq() != 0 || len(ack.GetHubAddresses()) != 1 || ack.GetHubAddresses()[0] != "hub.example:7443" {
		t.Errorf("HelloAck = %v", ack)
	}
	if got := ack.GetHubTime().AsTime(); !got.Equal(h.clock.now()) {
		t.Errorf("hub_time %v, want the hub's clock %v", got, h.clock.now())
	}
	if got := h.conns(t, m.id); len(got) != 1 || got[0].epoch != 1 || got[0].endReason != "" {
		t.Errorf("rows %+v, want one live row at epoch 1", got)
	}
	lines := h.events(t, "reconnect.hello")
	if len(lines) != 1 || lines[0]["machine_id"] != m.id || lines[0]["epoch"] != float64(1) || lines[0]["reason"] != "start" {
		t.Errorf("reconnect.hello lines %v", lines)
	}
	body := scrape(t, h.fixture)
	if v := sample(body, "fleet_hub_reconnects_total", `reason="start"`, `machine="`+m.id+`"`); v != 1 {
		t.Errorf("fleet_hub_reconnects_total{reason=start} = %v, want 1", v)
	}
	if v := sample(body, "fleet_hub_epoch", `machine="`+m.id+`"`); v != 1 {
		t.Errorf("fleet_hub_epoch = %v, want 1", v)
	}
	s.silent(t, 100*time.Millisecond)
}

// The epoch is max(stored, Hello.last_epoch) + 1 (02i): an agent ahead of
// the hub (a restored store) gets the next one above its own.
func TestConnectEpochFollowsTheAgentsLastEpoch(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 7))
	if got := s.ack(t).GetEpoch(); got != 8 {
		t.Fatalf("epoch %d, want 8", got)
	}
	if v := sample(scrape(t, h.fixture), "fleet_hub_epoch", `machine="`+m.id+`"`); v != 8 {
		t.Errorf("fleet_hub_epoch = %v, want 8", v)
	}
}

// A second Hello for the machine wins: the old stream gets
// Bye{DUPLICATE_IDENTITY} and ends, its row is closed as displaced with
// the heartbeat age it had (its Heartbeats reached the hub's memory), the
// new stream stays, and the duplicate is logged and counted once.
// Heartbeats write nothing to the store, and between the two HelloAcks the
// writer runs exactly one job, the second Hello's.
func TestASecondHelloDisplacesTheFirst(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	first := h.open(t, m)
	first.send(t, hello(m, 0))
	first.ack(t)
	jobs := metricSum(scrape(t, h.fixture), "fleet_hub_writer_jobs_total")
	for range 5 {
		first.send(t, heartbeat())
	}
	// The reader takes messages in order, so once the Event after them is
	// counted the Heartbeats were taken.
	first.send(t, unhandledMsg())
	h.waitMetric(t, "fleet_hub_connect_unhandled_total", 1, `kind="settings_ack"`)
	if got := metricSum(scrape(t, h.fixture), "fleet_hub_writer_jobs_total"); got != jobs {
		t.Errorf("heartbeats ran %v writer jobs, want none", got-jobs)
	}
	h.clock.set(h.clock.now().Add(6 * time.Second)) // past the Hello rate limit
	second := h.open(t, m)
	second.send(t, hello(m, 1))
	if got := second.ack(t).GetEpoch(); got != 2 {
		t.Fatalf("second epoch %d, want 2", got)
	}
	if got := metricSum(scrape(t, h.fixture), "fleet_hub_writer_jobs_total") - jobs; got != 1 {
		t.Errorf("%v writer jobs between the HelloAcks, want 1 (heartbeats write nothing)", got)
	}
	if got := first.bye(t); got != agentv1.ByeReason_BYE_REASON_DUPLICATE_IDENTITY {
		t.Fatalf("old stream Bye %v, want DUPLICATE_IDENTITY", got)
	}
	rows := h.conns(t, m.id)
	if len(rows) != 2 || rows[0].endReason != "displaced" || !rows[0].hbAge.Valid || rows[1].endReason != "" {
		t.Errorf("rows %+v, want epoch 1 displaced with a heartbeat age, epoch 2 live", rows)
	}
	second.send(t, heartbeat())
	second.silent(t, 200*time.Millisecond)
	if v := sample(scrape(t, h.fixture), "fleet_hub_duplicate_identity_total", `machine="`+m.id+`"`); v != 1 {
		t.Errorf("fleet_hub_duplicate_identity_total = %v, want 1", v)
	}
	lines := h.events(t, "identity.duplicate")
	if len(lines) != 1 || lines[0]["old_epoch"] != float64(1) || lines[0]["new_epoch"] != float64(2) {
		t.Errorf("identity.duplicate lines %v", lines)
	}
}

// A row a crash left live, which no stream holds, is displaced in the
// store without a Bye or a duplicate count.
func TestAHelloOverALeftoverLiveRowCountsNoDuplicate(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	h.exec(t, `INSERT INTO machine_connections(machine_id, epoch, boot_id, install_id, peer, connect_reason, started_time)
		VALUES (?, 4, 'boot-0', 'install-1', '127.0.0.1:1', 'start', 0)`, m.id)
	h.exec(t, `UPDATE machines SET last_epoch = 4 WHERE machine_id = ?`, m.id)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	if got := s.ack(t).GetEpoch(); got != 5 {
		t.Fatalf("epoch %d, want 5", got)
	}
	if v := sample(scrape(t, h.fixture), "fleet_hub_duplicate_identity_total"); v > 0 {
		t.Errorf("fleet_hub_duplicate_identity_total = %v, want none", v)
	}
	if n := len(h.events(t, "identity.duplicate")); n != 0 {
		t.Errorf("%d identity.duplicate lines, want 0", n)
	}
}

// The agent's Bye ends the stream at once: the hub closes it and the row
// is closed as agent_bye.
func TestTheAgentsByeClosesTheStream(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	s.send(t, &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_Bye{Bye: &agentv1.Bye{
		Reason: agentv1.ByeReason_BYE_REASON_SHUTDOWN,
	}}})
	if err := s.ended(t); err != nil {
		t.Fatalf("stream end %v, want clean", err)
	}
	h.waitConns(t, m.id, "the agent_bye row", func(c []conn) bool { return len(c) == 1 && c[0].endReason == "agent_bye" })
}

// A message of an arm with no handler yet is counted and logged once per
// kind per stream, and never ends the stream.
func TestAnUnhandledArmIsCountedAndIgnored(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	for range 3 {
		s.send(t, unhandledMsg())
	}
	h.waitMetric(t, "fleet_hub_connect_unhandled_total", 3, `kind="settings_ack"`)
	if lines := h.events(t, "hub.connect.unhandled"); len(lines) != 1 || lines[0]["kind"] != "settings_ack" {
		t.Errorf("hub.connect.unhandled lines %v, want one for settings_ack", lines)
	}
	s.send(t, heartbeat())
	s.silent(t, 100*time.Millisecond)
}

// Shutdown says Bye{HUB_RESTART} on every live stream, each handler
// returns, rows close as hub_restart, and Shutdown finishes well inside
// fleetd's 10 s grace (row 5 ruling, amendment 4).
func TestShutdownSaysHubRestartAndReturns(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	ms := []machine{h.enrollMachine(t), h.enrollMachine(t)}
	var streams []*agentStream
	for _, m := range ms {
		s := h.open(t, m)
		s.send(t, hello(m, 0))
		s.ack(t)
		streams = append(streams, s)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if err := h.srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("Shutdown took %v with live streams, want well under the 10 s grace", took)
	}
	for i, s := range streams {
		if got := s.bye(t); got != agentv1.ByeReason_BYE_REASON_HUB_RESTART {
			t.Errorf("stream %d Bye %v, want HUB_RESTART", i, got)
		}
		if rows := h.conns(t, ms[i].id); len(rows) != 1 || rows[0].endReason != "hub_restart" {
			t.Errorf("machine %d rows %+v, want one closed as hub_restart", i, rows)
		}
	}
}
