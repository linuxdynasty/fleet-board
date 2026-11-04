package api_test

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/attach"
)

// wsClient is a raw WebSocket client over TCP: the tests speak the exact
// bytes a browser would, so the handshake, framing and close codes are
// proven on the wire.
type wsClient struct {
	t    *testing.T
	conn net.Conn
	br   *bufio.Reader
}

func wsDial(t *testing.T, e *fleetEnv, origin, subprotocol string) *wsClient {
	t.Helper()
	host := strings.TrimPrefix(e.wsURL, "ws://")
	host = strings.TrimPrefix(host, "wss://")
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(context.Background(), "tcp", host)
	if err != nil {
		t.Fatalf("dial %s: %v", host, err)
	}
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
	var sb strings.Builder
	fmt.Fprintf(&sb, "GET /fleet.hub.v1.FleetService/AttachTerminal HTTP/1.1\r\n")
	fmt.Fprintf(&sb, "Host: %s\r\n", host)
	fmt.Fprintf(&sb, "Upgrade: websocket\r\n")
	fmt.Fprintf(&sb, "Connection: Upgrade\r\n")
	fmt.Fprintf(&sb, "Sec-WebSocket-Key: %s\r\n", key)
	fmt.Fprintf(&sb, "Sec-WebSocket-Version: 13\r\n")
	if subprotocol != "" {
		fmt.Fprintf(&sb, "Sec-WebSocket-Protocol: %s\r\n", subprotocol)
	}
	if origin != "" {
		fmt.Fprintf(&sb, "Origin: %s\r\n", origin)
	}
	fmt.Fprintf(&sb, "\r\n")
	if _, err := conn.Write([]byte(sb.String())); err != nil {
		t.Fatalf("write handshake: %v", err)
	}
	c := &wsClient{t: t, conn: conn, br: bufio.NewReader(conn)}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	status, headers := c.readHeaders()
	if !strings.Contains(status, "101") {
		t.Fatalf("handshake status = %q, headers = %v", status, headers)
	}
	if proto := headers["sec-websocket-protocol"]; proto != "fleet.attach.v1" {
		t.Fatalf("subprotocol = %q", proto)
	}
	_ = conn.SetReadDeadline(time.Time{})
	return c
}

// wsDialRefused performs the handshake and expects a plain-HTTP refusal.
func wsDialRefused(t *testing.T, e *fleetEnv, origin, subprotocol string) (int, string) {
	t.Helper()
	host := strings.TrimPrefix(e.wsURL, "ws://")
	host = strings.TrimPrefix(host, "wss://")
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(context.Background(), "tcp", host)
	if err != nil {
		t.Fatalf("dial %s: %v", host, err)
	}
	defer func() { _ = conn.Close() }()
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
	var sb strings.Builder
	fmt.Fprintf(&sb, "GET /fleet.hub.v1.FleetService/AttachTerminal HTTP/1.1\r\n")
	fmt.Fprintf(&sb, "Host: %s\r\n", host)
	fmt.Fprintf(&sb, "Upgrade: websocket\r\n")
	fmt.Fprintf(&sb, "Connection: Upgrade\r\n")
	fmt.Fprintf(&sb, "Sec-WebSocket-Key: %s\r\n", key)
	fmt.Fprintf(&sb, "Sec-WebSocket-Version: 13\r\n")
	if subprotocol != "" {
		fmt.Fprintf(&sb, "Sec-WebSocket-Protocol: %s\r\n", subprotocol)
	}
	if origin != "" {
		fmt.Fprintf(&sb, "Origin: %s\r\n", origin)
	}
	fmt.Fprintf(&sb, "\r\n")
	if _, err := conn.Write([]byte(sb.String())); err != nil {
		t.Fatalf("write handshake: %v", err)
	}
	br := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	parts := strings.SplitN(strings.TrimSpace(line), " ", 3)
	if len(parts) < 2 {
		t.Fatalf("status = %q", line)
	}
	code, err := strconv.Atoi(parts[1])
	if err != nil {
		t.Fatalf("status = %q", line)
	}
	var body strings.Builder
	for {
		h, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read headers: %v", err)
		}
		if h == "\r\n" {
			break
		}
	}
	// Refusals carry a Connect JSON error; keep the head for the reason.
	head := make([]byte, 512)
	n, _ := br.Read(head)
	body.Write(head[:n])
	return code, body.String()
}

func (c *wsClient) readHeaders() (string, map[string]string) {
	c.t.Helper()
	status, err := c.br.ReadString('\n')
	if err != nil {
		c.t.Fatalf("read status: %v", err)
	}
	headers := map[string]string{}
	for {
		line, err := c.br.ReadString('\n')
		if err != nil {
			c.t.Fatalf("read headers: %v", err)
		}
		if line == "\r\n" {
			return strings.TrimSpace(status), headers
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			headers[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
}

// sendFrame writes one masked client frame.
func (c *wsClient) sendFrame(opcode byte, payload []byte) {
	c.t.Helper()
	var head []byte
	head = append(head, 0x80|opcode)
	mask := []byte{0x1, 0x2, 0x3, 0x4}
	// Test payloads are small; the byte casts only frame them.
	n := len(payload)
	b0 := byte(n)      //nolint:gosec // G115: test framing
	b1 := byte(n >> 8) //nolint:gosec // G115: as above
	switch {
	case n < 126:
		head = append(head, 0x80|b0)
	case n < 1<<16:
		head = append(head, 0x80|126, b1, b0)
	default:
		b2 := byte(n >> 16) //nolint:gosec // G115: as above
		b3 := byte(n >> 24) //nolint:gosec // G115: as above
		head = append(head, 0x80|127, 0, 0, 0, 0, b3, b2, b1, b0)
	}
	head = append(head, mask...)
	masked := make([]byte, len(payload))
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.conn.Write(append(head, masked...)); err != nil {
		c.t.Fatalf("write frame: %v", err)
	}
}

// sendMessage marshals one request into one binary frame.
func (c *wsClient) sendMessage(msg *hubv1.AttachTerminalRequest) {
	c.t.Helper()
	body, err := proto.Marshal(msg)
	if err != nil {
		c.t.Fatalf("marshal: %v", err)
	}
	c.sendFrame(0x2, body)
}

// readMessage reads server frames until one binary message arrives,
// answering pings on the way like a browser would.
func (c *wsClient) readMessage() *hubv1.AttachTerminalResponse {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		h0, err := c.br.ReadByte()
		if err != nil {
			c.t.Fatalf("read opcode: %v", err)
		}
		h1, err := c.br.ReadByte()
		if err != nil {
			c.t.Fatalf("read length: %v", err)
		}
		opcode := h0 & 0x0F
		length := int(h1 & 0x7F)
		switch length {
		case 126:
			var ext [2]byte
			if _, err := c.br.Read(ext[:]); err != nil {
				c.t.Fatalf("read ext: %v", err)
			}
			length = int(ext[0])<<8 | int(ext[1])
		case 127:
			var ext [8]byte
			if _, err := c.br.Read(ext[:]); err != nil {
				c.t.Fatalf("read ext64: %v", err)
			}
			length = 0
			for _, b := range ext {
				length = length<<8 | int(b)
			}
		}
		payload := make([]byte, length)
		for off := 0; off < length; {
			n, err := c.br.Read(payload[off:])
			off += n
			if err != nil {
				c.t.Fatalf("read payload: %v", err)
			}
		}
		switch opcode {
		case 0x2:
			var msg hubv1.AttachTerminalResponse
			if err := proto.Unmarshal(payload, &msg); err != nil {
				c.t.Fatalf("unmarshal: %v", err)
			}
			_ = c.conn.SetReadDeadline(time.Time{})
			return &msg
		case 0x9:
			c.sendFrame(0xA, payload)
		case 0x8:
			code := 1005
			if len(payload) >= 2 {
				code = int(payload[0])<<8 | int(payload[1])
			}
			c.t.Fatalf("socket closed with code %d while reading a message", code)
		}
	}
}

// readCloseCode reads the closing frame's code.
func (c *wsClient) readCloseCode() int {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		h0, err := c.br.ReadByte()
		if err != nil {
			c.t.Fatalf("read opcode: %v", err)
		}
		h1, err := c.br.ReadByte()
		if err != nil {
			c.t.Fatalf("read length: %v", err)
		}
		opcode := h0 & 0x0F
		length := int(h1 & 0x7F)
		if length == 126 {
			var ext [2]byte
			if _, err := c.br.Read(ext[:]); err != nil {
				c.t.Fatalf("read ext: %v", err)
			}
			length = int(ext[0])<<8 | int(ext[1])
		}
		payload := make([]byte, length)
		for off := 0; off < length; {
			n, err := c.br.Read(payload[off:])
			off += n
			if err != nil {
				c.t.Fatalf("read payload: %v", err)
			}
		}
		switch opcode {
		case 0x8:
			if len(payload) < 2 {
				return 1005
			}
			return int(payload[0])<<8 | int(payload[1])
		case 0x9:
			c.sendFrame(0xA, payload)
		case 0x2:
			// A message before the close: the close follows.
			continue
		}
	}
}

func (c *wsClient) close() {
	_ = c.conn.Close()
}

// wsAttachEnv builds the h1 hub a browser would reach, with a ticket
// minted for the dashboard origin.
func wsAttachEnv(t *testing.T, reg *attach.Registry, reqID string) (*fleetEnv, string, string) {
	t.Helper()
	e := newFleetEnv(t, localUser("alice"),
		withAttach(reg), withAttachReadyWait(5*time.Second),
		withMachineLive(func(id string) bool { return id == "m1" }))
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	res, err := e.client.CreateAttachTicket(context.Background(), ticketReq(reqID, sess, false))
	if err != nil {
		t.Fatalf("CreateAttachTicket: %v", err)
	}
	return e, sess, res.Msg.GetTicket()
}

// TestAttachWSRoundTrip: the browser handshake, ticket burn, keystrokes,
// output, close and the 1000 close code, all on the wire.
func TestAttachWSRoundTrip(t *testing.T) {
	t.Parallel()
	reg := attach.New(time.Now, nil)
	e, sess, ticket := wsAttachEnv(t, reg, "req-ws-1")
	c := wsDial(t, e, "http://localhost:8080", "fleet.attach.v1")
	defer c.close()
	c.sendMessage(&hubv1.AttachTerminalRequest{Body: &hubv1.AttachTerminalRequest_Open{Open: &hubv1.AttachOpen{
		RequestId: "req-ws-open-1", Session: &typev1.SessionId{Value: sess},
		Cols: 80, Rows: 24, Ticket: ticket,
	}}})

	// The bridge the open made pairs with the agent's Terminal stream.
	b := waitBridge(t, e, reg)
	agent := newFakeTerminal()
	agent.recv <- termReadyUp(b.ID(), "fleet-view-"+b.ID(), 80, 24)
	agentErr := make(chan error, 1)
	go func() { agentErr <- reg.ServeAgent(context.Background(), "m1", agent) }()

	ready := c.readMessage()
	if ready.GetReady() == nil || ready.GetReady().GetAttachId().GetValue() != b.ID() {
		t.Fatalf("ready = %v", ready)
	}

	// Keystrokes reach the agent; output reaches the browser.
	c.sendMessage(&hubv1.AttachTerminalRequest{
		Body: &hubv1.AttachTerminalRequest_Input{Input: []byte("echo hi\n")},
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		var found bool
		for _, d := range agent.downs() {
			if string(d.GetInput()) == "echo hi\n" {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent never got the keystrokes")
		}
		time.Sleep(5 * time.Millisecond)
	}
	agent.recv <- termOutputUp(b.ID(), []byte("hi\n"))
	if out := c.readMessage(); string(out.GetOutput()) != "hi\n" {
		t.Fatalf("browser output = %q", out.GetOutput())
	}
	c.sendMessage(&hubv1.AttachTerminalRequest{
		Body: &hubv1.AttachTerminalRequest_Ack{Ack: &hubv1.AttachAck{BytesWritten: 3}},
	})

	// Closing the tab detaches with DETACHED and a 1000 close.
	c.sendMessage(&hubv1.AttachTerminalRequest{
		Body: &hubv1.AttachTerminalRequest_Close{Close: &hubv1.AttachClose{}},
	})
	deadline = time.Now().Add(5 * time.Second)
	for {
		var found bool
		for _, d := range agent.downs() {
			if d.GetClose() != nil {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent never got the detach")
		}
		time.Sleep(5 * time.Millisecond)
	}
	agent.recv <- termClosedUp(b.ID(), typev1.AttachCloseReason_ATTACH_CLOSE_REASON_DETACHED)
	if closed := c.readMessage(); closed.GetClosed().GetReason() != typev1.AttachCloseReason_ATTACH_CLOSE_REASON_DETACHED {
		t.Fatalf("close = %v", closed)
	}
	if code := c.readCloseCode(); code != 1000 {
		t.Fatalf("close code = %d, want 1000", code)
	}
	select {
	case err := <-agentErr:
		if err != nil {
			t.Fatalf("ServeAgent: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("agent stream did not end")
	}
}

// TestAttachWSRefusals: a missing subprotocol or Origin never reaches the
// handshake; a bad ticket burns the open with 1008.
func TestAttachWSRefusals(t *testing.T) {
	t.Parallel()
	reg := attach.New(time.Now, nil)
	e, _, _ := wsAttachEnv(t, reg, "req-ws-2")

	if code, _ := wsDialRefused(t, e, "http://localhost:8080", ""); code != 400 {
		t.Fatalf("missing subprotocol status = %d, want 400", code)
	}
	if code, _ := wsDialRefused(t, e, "", "fleet.attach.v1"); code != 403 {
		t.Fatalf("missing origin status = %d, want 403", code)
	}

	c := wsDial(t, e, "http://localhost:8080", "fleet.attach.v1")
	defer c.close()
	c.sendMessage(&hubv1.AttachTerminalRequest{Body: &hubv1.AttachTerminalRequest_Open{Open: &hubv1.AttachOpen{
		RequestId: "req-ws-bad-1", Session: &typev1.SessionId{Value: "sess-1"},
		Cols: 80, Rows: 24, Ticket: "bad-ticket",
	}}})
	closed := c.readMessage()
	err := closed.GetClosed().GetError()
	if closed.GetClosed().GetReason() != typev1.AttachCloseReason_ATTACH_CLOSE_REASON_ERROR ||
		err.GetReason() != typev1.ReasonCode_REASON_CODE_ATTACH_TICKET_INVALID {
		t.Fatalf("bad ticket close = %v", closed)
	}
	if code := c.readCloseCode(); code != 1008 {
		t.Fatalf("bad ticket close code = %d, want 1008", code)
	}
}

// TestAttachWSFraming: a text frame closes 1003 and an over-large frame
// 1009, whatever the open said.
func TestAttachWSFraming(t *testing.T) {
	t.Parallel()
	reg := attach.New(time.Now, nil)
	e, _, _ := wsAttachEnv(t, reg, "req-ws-3")

	c := wsDial(t, e, "http://localhost:8080", "fleet.attach.v1")
	defer c.close()
	c.sendFrame(0x1, []byte("hello"))
	if code := c.readCloseCode(); code != 1003 {
		t.Fatalf("text frame close code = %d, want 1003", code)
	}

	c2 := wsDial(t, e, "http://localhost:8080", "fleet.attach.v1")
	defer c2.close()
	c2.sendFrame(0x2, make([]byte, 9<<10))
	if code := c2.readCloseCode(); code != 1009 {
		t.Fatalf("oversize frame close code = %d, want 1009", code)
	}
}
