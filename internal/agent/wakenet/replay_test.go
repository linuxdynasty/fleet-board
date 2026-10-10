package wakenet

import (
	"bufio"
	"encoding/json"
	"net/netip"
	"os"
	"testing"
	"time"
)

// pushRecord is one push line of the spike's linux-box log
// (spikes/wakenet/results/linux-box-live-extract.jsonl, copied to testdata/).
type pushRecord struct {
	at  time.Time
	msg []byte
}

// loadLinuxBox rebuilds each logged netlink message as the kernel sent it.
// The cut-off line at the log gap is not valid JSON and is skipped, as the
// spike's counting script did.
func loadLinuxBox(t *testing.T) []pushRecord {
	t.Helper()
	f, err := os.Open("testdata/linux-box-live-extract.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var recs []pushRecord
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var line struct {
			Time   time.Time `json:"time"`
			Event  string    `json:"event"`
			Method string    `json:"method"`
			Index  uint32    `json:"index"`
			Detail string    `json:"detail"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil || line.Method != "push" {
			continue
		}
		var msg []byte
		switch line.Event {
		case "RTM_NEWADDR":
			msg = nlMsg(nlNewAddr, ifaddrmsg(afInet6Linux, 64, line.Index))
		case "RTM_NEWLINK":
			flags := uint32(0)
			if line.Detail == "up" {
				flags = 0x41
			}
			msg = nlMsg(nlNewLink, ifinfomsg(line.Index, flags))
		default:
			continue
		}
		recs = append(recs, pushRecord{at: line.Time, msg: msg})
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return recs
}

// TestLinuxBoxReplay is the network half of capability A6: it replays every
// recorded push message from linux-box (ADR 0029, plan row 4). The
// IPv6 refreshes of enp3s0 change nothing and must never start a reconnect or
// reset the backoff; the one real change, tun0 coming up, is one event.
func TestLinuxBoxReplay(t *testing.T) {
	t.Parallel()
	recs := loadLinuxBox(t)
	if len(recs) != 10 {
		t.Fatalf("fixture has %d push messages, want 10", len(recs))
	}
	t0 := recs[0].at
	tunUp := time.Date(2026, 10, 8, 5, 10, 30, 0, time.FixedZone("EDT", -4*3600)).Sub(t0)
	withTun := homeReading()
	withTun.Interfaces["tun0"] = []netip.Prefix{pfx("10.8.0.2/24")}

	g := newNetRig(t, homeReading(), nil, nil)
	g.logs.waitFor(t, "netchange.baseline")

	var pending *fakeTimer
	var due time.Duration
	var events []Event
	settle := func() {
		if g.settle(t, pending, due) == "netchange.detected" {
			events = append(events, <-g.out)
		}
		pending = nil
	}
	for _, r := range recs {
		at := r.at.Sub(t0)
		if pending != nil && due <= at {
			settle()
		}
		if at >= tunUp {
			g.reader.set(withTun, nil)
		}
		g.clock.setMono(at)
		g.sock.reads <- r.msg
		pending = g.clock.nextTimer(t)
		due = at + pending.d
	}
	settle()

	if n := g.reader.count(); n != 8 {
		t.Errorf("read %d times, want 8: the baseline and one per quiet group of hints", n)
	}
	if n := g.logs.count("netchange.unchanged"); n != 6 {
		t.Errorf("netchange.unchanged logged %d times, want 6 (each refresh group)", n)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 (tun0 up)", len(events))
	}
	if ch := events[0].Net.Changes; len(ch) != 1 || ch[0] != (Change{Kind: InterfaceAdded, Iface: "tun0"}) {
		t.Errorf("changes = %+v, want tun0 added", ch)
	}
	offline := Decide(events[0], Conn{})
	if !offline.Reconnect || offline.Reason != reasonNet {
		t.Errorf("not connected: %+v, want a reset and an attempt with NETWORK_CHANGE", offline)
	}
	online := Decide(events[0], Conn{Connected: true, LocalAddr: addr("192.168.1.50")})
	if online.Reconnect {
		t.Errorf("connected over enp3s0: %+v, want no reconnect for a VPN interface coming up", online)
	}
}
