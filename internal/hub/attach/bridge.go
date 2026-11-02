package attach

import (
	"errors"
	"sync"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// Flow-control and framing limits (02f, defaults.md, ADR 0016).
const (
	// HighWater is the unacked output that suspends the agent side: the
	// gateway stops reading the Terminal stream, so HTTP/2 flow control
	// blocks the agent's PTY read and no output is dropped.
	HighWater = 256 << 10
	// OutputChunk is the most output one AttachTerminalResponse carries.
	OutputChunk = 64 << 10
	// InputMax is the most input one AttachTerminalRequest carries.
	InputMax = 4 << 10
	// ClientFrameMax is the biggest WebSocket frame the hub accepts.
	ClientFrameMax = 8 << 10
	// HubFrameMax is the biggest frame the hub sends (output plus its
	// envelope).
	HubFrameMax = 66 << 10
)

// ErrBridgeClosed is PushOutput and TakeInput past Close.
var ErrBridgeClosed = errors.New("attach: bridge is closed")

// BridgeState is where an attach stands: OpenAttach sent, the agent's
// Terminal stream paired and flowing, or over.
type BridgeState int

const (
	// BridgePending waits for the agent's TermReady.
	BridgePending BridgeState = iota
	// BridgeLive flows both ways.
	BridgeLive
	// BridgeClosed is terminal.
	BridgeClosed
)

// Bridge pairs one AttachTerminal client with one agent Terminal stream.
// The zero value is useless; build through [Registry.Create]. Every
// method is safe for the bridge's two pumps at once.
type Bridge struct {
	mu        sync.Mutex
	changed   chan struct{}
	closed    chan struct{}
	id        string
	machineID string
	sessionID string
	commandID string
	readOnly  bool
	state     BridgeState
	view      string
	cols      uint32
	rows      uint32
	claimed   bool
	gen       int
	finished  bool
	idle      time.Duration
	output    [][]byte
	outBytes  int
	down      []*agentv1.TermDown
	unacked   uint64
	reason    typev1.AttachCloseReason
	detail    *typev1.ErrorDetail
}

// ID, MachineID, SessionID and ReadOnly describe the attach.
func (b *Bridge) ID() string        { return b.id }
func (b *Bridge) MachineID() string { return b.machineID }
func (b *Bridge) SessionID() string { return b.sessionID }
func (b *Bridge) ReadOnly() bool    { return b.readOnly }
func (b *Bridge) View() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.view
}

// CommandID is the OpenAttach command that opened the attach, for the
// audit trail and AttachReady. SetCommand sets it once, in the open
// transaction that enqueued the command.
func (b *Bridge) CommandID() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.commandID
}

// SetMachine records the agent's machine resolved in the open
// transaction; ServeAgent pairs only that machine's stream.
func (b *Bridge) SetMachine(machineID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.machineID == "" {
		b.machineID = machineID
	}
}

// SetCommand records the OpenAttach command id; the first call wins.
func (b *Bridge) SetCommand(commandID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.commandID == "" {
		b.commandID = commandID
	}
}

// Claim takes the bridge for one agent Terminal stream. Only one stream
// pairs with an attach: a second dialer gets false and backs off.
func (b *Bridge) Claim() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.claimed || b.state == BridgeClosed {
		return false
	}
	b.claimed = true
	return true
}

// Release frees the claim; the next dial may pair.
func (b *Bridge) Release() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.claimed = false
	b.broadcastLocked()
}

// TakeOver takes the bridge for a new client pump: a retried open adopts
// the live bridge, and the older pump (a dead stream's remainder) sees
// its generation go stale and stops. Returns the pump's generation.
func (b *Bridge) TakeOver() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.gen++
	b.broadcastLocked()
	return b.gen
}

// Finish takes the bridge for exactly one finisher: the attach closes,
// audits and forgets once no matter how many pumps race its end. Later
// finishers only read the terminal state.
func (b *Bridge) Finish() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finished {
		return false
	}
	b.finished = true
	return true
}

// Generation is the current client generation.
func (b *Bridge) Generation() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.gen
}

// SetIdle records the fleet layer's attach_idle_timeout the open read;
// the first open's value wins, so an adopted retry shares it.
func (b *Bridge) SetIdle(d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.idle <= 0 {
		b.idle = d
	}
}

// Idle is the no-input-or-output wait before an IDLE_TIMEOUT detach.
func (b *Bridge) Idle() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.idle
}

func (b *Bridge) Size() (uint32, uint32) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cols, b.rows
}

// Ready pairs the agent's Terminal stream: the PTY is open, output may
// flow. The first TermUp must be ready; anything before it is a protocol
// error the gateway refuses.
func (b *Bridge) Ready(view string, cols, rows uint32) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state != BridgePending {
		return
	}
	b.view, b.cols, b.rows = view, cols, rows
	b.state = BridgeLive
	b.broadcastLocked()
}

// Live reports whether output and input may flow.
func (b *Bridge) Live() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state == BridgeLive
}

// PushOutput queues agent output for the client and counts it unacked.
// Past the high-water mark the gateway stops reading the agent, so this
// is where a slow browser applies backpressure to the Terminal stream
// only: Connect commands are another stream and never wait here.
func (b *Bridge) PushOutput(p []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == BridgeClosed {
		return ErrBridgeClosed
	}
	if len(p) == 0 {
		return nil
	}
	cp := make([]byte, len(p))
	copy(cp, p)
	b.output = append(b.output, cp)
	b.outBytes += len(cp)
	b.unacked += uint64(len(cp))
	b.broadcastLocked()
	return nil
}

// Suspended reports whether unacked output reached the high-water mark.
func (b *Bridge) Suspended() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.unacked >= HighWater
}

// NextOutput pops up to max queued output bytes for one client message,
// splitting a queued chunk when it overflows the message.
func (b *Bridge) NextOutput(max int) []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []byte
	for len(b.output) > 0 && len(out) < max {
		head := b.output[0]
		if room := max - len(out); len(head) > room {
			out = append(out, head[:room]...)
			b.output[0] = head[room:]
			b.outBytes -= room
			break
		}
		out = append(out, head...)
		b.output = b.output[1:]
		b.outBytes -= len(head)
	}
	return out
}

// OutputBytes is the queued output waiting for the client.
func (b *Bridge) OutputBytes() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.outBytes
}

// Ack counts client-written bytes: the AttachAck flow control (02f).
func (b *Bridge) Ack(n uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > b.unacked {
		b.unacked = 0
	} else {
		b.unacked -= n
	}
	b.broadcastLocked()
}

// Unacked is the output sent but not yet acked.
func (b *Bridge) Unacked() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.unacked
}

// TakeInput queues keystrokes for the agent, or drops them on a read-only
// attach (the hub drops viewer input; the agent also refuses it).
func (b *Bridge) TakeInput(p []byte) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == BridgeClosed || b.readOnly || len(p) == 0 {
		return false
	}
	cp := make([]byte, len(p))
	copy(cp, p)
	b.down = append(b.down, &agentv1.TermDown{
		AttachId: &typev1.AttachId{Value: b.id},
		Body:     &agentv1.TermDown_Input{Input: cp},
	})
	b.broadcastLocked()
	return true
}

// TakeResize queues a PTY resize for the agent's view session.
func (b *Bridge) TakeResize(cols, rows uint32) bool {
	return b.queue(&agentv1.TermDown{
		Body: &agentv1.TermDown_Resize{Resize: &agentv1.TermResize{Cols: cols, Rows: rows}},
	})
}

// TakeSelectWindow queues a view-session window switch. The key must
// belong to the attached session; the agent runs it in the view session
// only, so the developer's own current window never moves.
func (b *Bridge) TakeSelectWindow(windowID string) bool {
	return b.queue(&agentv1.TermDown{
		Body: &agentv1.TermDown_Select{Select: &agentv1.TermSelect{
			Target: &agentv1.TermSelect_Window{Window: &typev1.WindowKey{WindowId: windowID}},
		}},
	})
}

// TakeSelectPane queues a view-session pane switch.
func (b *Bridge) TakeSelectPane(server, startID, paneID string) bool {
	return b.queue(&agentv1.TermDown{
		Body: &agentv1.TermDown_Select{Select: &agentv1.TermSelect{
			Target: &agentv1.TermSelect_Pane{Pane: &typev1.PaneKey{Server: server, ServerStartId: startID, PaneId: paneID}},
		}},
	})
}

// TakeClose queues the agent-side detach. It does not close the bridge:
// the agent answers TermUp{closed} and the client gets AttachClosed first.
func (b *Bridge) TakeClose() bool {
	return b.queue(&agentv1.TermDown{Body: &agentv1.TermDown_Close{Close: &agentv1.TermClose{}}})
}

func (b *Bridge) queue(msg *agentv1.TermDown) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == BridgeClosed {
		return false
	}
	msg.AttachId = &typev1.AttachId{Value: b.id}
	b.down = append(b.down, msg)
	b.broadcastLocked()
	return true
}

// DrainDown takes every queued TermDown for the agent.
func (b *Bridge) DrainDown() []*agentv1.TermDown {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.down
	b.down = nil
	return out
}

// Close ends the attach with its reason (and the error detail when the
// reason is ERROR). It is terminal: pushes and takes refuse past it, and
// Done fires.
func (b *Bridge) Close(reason typev1.AttachCloseReason, detail *typev1.ErrorDetail) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == BridgeClosed {
		return
	}
	b.state = BridgeClosed
	b.reason, b.detail = reason, detail
	b.output = nil
	b.outBytes = 0
	close(b.closed)
	b.broadcastLocked()
}

// Reason is the close reason; Detail carries the error for ERROR closes.
func (b *Bridge) Reason() typev1.AttachCloseReason {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reason
}

// Detail is the error detail for ERROR closes, nil otherwise.
func (b *Bridge) Detail() *typev1.ErrorDetail {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.detail
}

// Done fires when the bridge closes.
func (b *Bridge) Done() <-chan struct{} {
	return b.closed
}

// Changed fires whenever the bridge gains output, TermDowns, acks or
// readiness: a pump re-reads after every fire. It fires spuriously; a
// pump always re-checks state after waking.
func (b *Bridge) Changed() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.changed
}

func (b *Bridge) broadcastLocked() {
	close(b.changed)
	b.changed = make(chan struct{})
}
