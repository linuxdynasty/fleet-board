package api

import (
	"bufio"
	"crypto/sha1" //nolint:gosec // G505: RFC 6455 mandates SHA-1 for the handshake accept key, not a security use.
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1/hubv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/attach"
)

// attachSubprotocol is the WebSocket subprotocol a browser must offer
// (02f): the hub refuses an upgrade that does not offer it.
const attachSubprotocol = "fleet.attach.v1"

// wsHandshakeTimeout bounds the whole pre-message exchange: TCP connect
// to first frame is the 10 s first-frame allowance, enforced by the
// serveAttach core.
const wsHandshakeTimeout = 10 * time.Second

// wsPingInterval is the 30 s hub ping; wsPongWait is the 10 s a pong may
// take before the hub closes a dead client (02f).
const (
	wsPingInterval = 30 * time.Second
	wsPongWait     = 10 * time.Second
)

// isAttachWSUpgrade reports whether r is a browser attach opening on the
// AttachTerminal procedure: the path plus an Upgrade: websocket header.
// Anything else on the procedure is a Connect bidi stream (a CLI over
// HTTP/2) and goes to the Connect handler.
func isAttachWSUpgrade(r *http.Request) bool {
	if r.URL.Path != hubv1connect.FleetServiceAttachTerminalProcedure {
		return false
	}
	for _, v := range r.Header.Values("Upgrade") {
		for _, token := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "websocket") {
				return true
			}
		}
	}
	return false
}

// serveAttachWS serves one browser attach: plain-HTTP 401/403 checks
// before the upgrade (02f), the RFC 6455 handshake, then the shared
// serveAttach core over the socket, and the WebSocket close code after.
func (f *Fleet) serveAttachWS(w http.ResponseWriter, r *http.Request) {
	errs := connect.NewErrorWriter()
	deny := func(code connect.Code, reason typev1.ReasonCode, msg string) {
		_ = errs.Write(w, r, apiError(code, reason, msg, nil, nil))
	}
	if r.Method != http.MethodGet {
		deny(connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "attach upgrade is a GET")
		return
	}
	if PrincipalFrom(r.Context()) == nil {
		deny(connect.CodeUnauthenticated, typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
			"attach needs a caller identity")
		return
	}
	if r.Header.Get("Origin") == "" {
		deny(connect.CodePermissionDenied, typev1.ReasonCode_REASON_CODE_ORIGIN_NOT_ALLOWED,
			"attach needs an Origin")
		return
	}
	if !offersSubprotocol(r.Header.Get("Sec-WebSocket-Protocol")) {
		deny(connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"attach needs the "+attachSubprotocol+" subprotocol")
		return
	}
	if !validWSVersion(r.Header.Get("Sec-WebSocket-Version")) {
		deny(connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"attach needs Sec-WebSocket-Version 13")
		return
	}
	accept, err := wsAcceptKey(r.Header.Get("Sec-WebSocket-Key"))
	if err != nil {
		deny(connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"attach needs a valid Sec-WebSocket-Key")
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		deny(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL, "attach needs a hijackable listener")
		return
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		deny(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL, "the hub could not take the connection")
		return
	}
	if _, err := rw.WriteString("HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Protocol: " + attachSubprotocol + "\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"); err != nil {
		_ = conn.Close()
		return
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return
	}
	ws := newWSConn(conn)
	origin := r.Header.Get("Origin")
	pingStop := make(chan struct{})
	var pingWG sync.WaitGroup
	pingWG.Add(1)
	go func() {
		defer pingWG.Done()
		ws.pingLoop(pingStop)
	}()
	closed, code := f.serveAttach(r.Context(), ws, transportWS, origin)
	close(pingStop)
	if closed != nil {
		_ = ws.sendMessage(&hubv1.AttachTerminalResponse{
			Body: &hubv1.AttachTerminalResponse_Closed{Closed: closed},
		})
		ws.closeSocket(code)
	} else {
		// Taken over: the new owner ends the attach. This transport
		// closes normally; a live client re-opens if it needs to.
		ws.closeSocket(1000)
	}
	pingWG.Wait()
}

// offersSubprotocol reports whether the header offers our subprotocol.
func offersSubprotocol(header string) bool {
	for _, p := range strings.Split(header, ",") {
		if strings.TrimSpace(p) == attachSubprotocol {
			return true
		}
	}
	return false
}

// validWSVersion accepts only RFC 6455.
func validWSVersion(header string) bool {
	return strings.TrimSpace(header) == "13"
}

// wsAcceptKey derives the handshake accept key (RFC 6455 §1.3).
func wsAcceptKey(key string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(key))
	if err != nil || len(raw) != 16 {
		return "", errors.New("api: bad Sec-WebSocket-Key")
	}
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11")) //nolint:gosec // G505: RFC 6455 mandates SHA-1 here.
	return base64.StdEncoding.EncodeToString(sum[:]), nil
}

// wsConn is one hijacked WebSocket as a clientConn: binary frames carry
// exactly one AttachTerminal message each way. Reads run on the receiver
// pump alone; writes (the sender pump, the pinger, the final close) share
// a mutex. A text frame closes 1003, an over-large frame 1009, a
// fragmented or otherwise malformed message 1007, an unmasked client
// frame 1002.
type wsConn struct {
	conn   net.Conn
	br     *bufio.Reader
	wmu    sync.Mutex
	pongCh chan struct{}
	closed chan struct{}
	once   sync.Once
}

func newWSConn(conn net.Conn) *wsConn {
	return &wsConn{conn: conn, br: bufio.NewReader(conn), pongCh: make(chan struct{}, 1), closed: make(chan struct{})}
}

// Send writes one message as one binary frame.
func (c *wsConn) Send(res *hubv1.AttachTerminalResponse) error {
	body, err := proto.Marshal(res)
	if err != nil {
		return err
	}
	return c.writeFrame(0x2, body)
}

// Receive reads one message: exactly one binary frame. Control frames
// are answered inline: pings get pongs, pongs wake the pinger, a peer
// close is echoed and ends the stream.
func (c *wsConn) Receive() (*hubv1.AttachTerminalRequest, error) {
	for {
		fin, opcode, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch opcode {
		case 0x8:
			code := wsPeerCode(payload)
			c.closeSocket(code)
			return nil, errPeerClose
		case 0x9:
			_ = c.writeFrame(0xA, payload)
			continue
		case 0xA:
			select {
			case c.pongCh <- struct{}{}:
			default:
			}
			continue
		case 0x2:
			if !fin {
				c.closeSocket(1007)
				return nil, errWSFraming
			}
			var msg hubv1.AttachTerminalRequest
			if err := proto.Unmarshal(payload, &msg); err != nil {
				c.closeSocket(1007)
				return nil, errWSFraming
			}
			return &msg, nil
		case 0x1:
			c.closeSocket(1003)
			return nil, errWSFraming
		default:
			c.closeSocket(1007)
			return nil, errWSFraming
		}
	}
}

// errPeerClose ends Receive when the peer hung up; errWSFraming when the
// hub closed a framing violation (the socket already carries the code).
var (
	errPeerClose = errors.New("api: peer closed the attach")
	errWSFraming = errors.New("api: framing violation")
)

// wsPeerCode reads the peer's close code, 1005 when absent.
func wsPeerCode(payload []byte) int {
	if len(payload) < 2 {
		return 1005
	}
	return int(payload[0])<<8 | int(payload[1])
}

// readFrame reads one WebSocket frame: fin, opcode and the unmasked
// payload. Client frames must be masked (1002 otherwise) and fit the
// 02f 8 KiB cap (1009). Control frames must fit 125 bytes and never
// fragment (1002 and 1009); continuation frames are refused (1007:
// one frame is exactly one message).
func (c *wsConn) readFrame() (bool, byte, []byte, error) {
	head := make([]byte, 2)
	if err := c.readFull(head); err != nil {
		return false, 0, nil, err
	}
	fin := head[0]&0x80 != 0
	opcode := head[0] & 0x0F
	masked := head[1]&0x80 != 0
	length := int(head[1] & 0x7F)
	switch {
	case opcode >= 0x8:
		if !fin || length > 125 {
			c.closeSocket(1002)
			return false, 0, nil, errWSFraming
		}
	case opcode == 0x0:
		c.closeSocket(1007)
		return false, 0, nil, errWSFraming
	case opcode != 0x1 && opcode != 0x2:
		c.closeSocket(1002)
		return false, 0, nil, errWSFraming
	}
	switch length {
	case 126:
		ext := make([]byte, 2)
		if err := c.readFull(ext); err != nil {
			return false, 0, nil, err
		}
		length = int(ext[0])<<8 | int(ext[1])
		if length < 126 {
			c.closeSocket(1002)
			return false, 0, nil, errWSFraming
		}
	case 127:
		ext := make([]byte, 8)
		if err := c.readFull(ext); err != nil {
			return false, 0, nil, err
		}
		if ext[0]&0x80 != 0 {
			c.closeSocket(1009)
			return false, 0, nil, errWSFraming
		}
		length = 0
		for _, b := range ext {
			length = length<<8 | int(b)
		}
	}
	if length > attach.ClientFrameMax {
		c.closeSocket(1009)
		return false, 0, nil, errWSFraming
	}
	if !masked {
		c.closeSocket(1002)
		return false, 0, nil, errWSFraming
	}
	var mask [4]byte
	if err := c.readFull(mask[:]); err != nil {
		return false, 0, nil, err
	}
	payload := make([]byte, length)
	if err := c.readFull(payload); err != nil {
		return false, 0, nil, err
	}
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	return fin, opcode, payload, nil
}

func (c *wsConn) readFull(p []byte) error {
	for len(p) > 0 {
		n, err := c.br.Read(p)
		p = p[n:]
		if err != nil {
			return err
		}
	}
	return nil
}

// writeFrame writes one server frame: server frames are never masked.
// Oversize hub frames are the hub's bug: refuse them instead of
// violating the 66 KiB cap on the wire.
func (c *wsConn) writeFrame(opcode byte, payload []byte) error {
	if len(payload) > attach.HubFrameMax {
		return errors.New("api: hub frame over the 66 KiB cap")
	}
	head := []byte{0x80 | opcode}
	// Lengths are safe: payload is capped at HubFrameMax above, so the
	// short cases hold at most 65,535 and the long case shifts in zero.
	n := len(payload)
	b0 := byte(n)      //nolint:gosec // G115: capped at HubFrameMax
	b1 := byte(n >> 8) //nolint:gosec // G115: as above
	switch {
	case n < 126:
		head = append(head, b0)
	case n < 1<<16:
		head = append(head, 126, b1, b0)
	default:
		b2 := byte(n >> 16) //nolint:gosec // G115: as above
		b3 := byte(n >> 24) //nolint:gosec // G115: as above
		head = append(head, 127, 0, 0, 0, 0, b3, b2, b1, b0)
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(wsHandshakeTimeout))
	_, err := writeFull(c.conn, head, payload)
	return err
}

func writeFull(conn net.Conn, parts ...[]byte) (int, error) {
	total := 0
	for _, p := range parts {
		for len(p) > 0 {
			n, err := conn.Write(p)
			total += n
			p = p[n:]
			if err != nil {
				return total, err
			}
		}
	}
	return total, nil
}

// sendMessage marshals one response into one binary frame.
func (c *wsConn) sendMessage(res *hubv1.AttachTerminalResponse) error {
	body, err := proto.Marshal(res)
	if err != nil {
		return err
	}
	return c.writeFrame(0x2, body)
}

// closeSocket writes the close frame once and closes the TCP connection.
// The close reason text stays empty: the typed AttachClosed frame already
// went out. Late and double closes are no-ops.
func (c *wsConn) closeSocket(code int) {
	c.once.Do(func() {
		close(c.closed)
		// Close codes are uint16 on the wire; the callers map to the
		// RFC 6455 constants, and anything else fails closed to 1011.
		if code < 1000 || code > 4999 {
			code = 1011
		}
		hi := byte(code >> 8) //nolint:gosec // G115: ranged above
		lo := byte(code)      //nolint:gosec // G115: as above
		_ = c.writeFrame(0x8, []byte{hi, lo})
		_ = c.conn.Close()
	})
}

// pingLoop pings every 30 s and closes the socket when no pong answers
// within 10 s: a dead browser must not hold an attach (or its PTY).
func (c *wsConn) pingLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(wsPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-c.closed:
			return
		case <-ticker.C:
			if err := c.writeFrame(0x9, nil); err != nil {
				c.closeSocket(1006)
				return
			}
			timer := time.NewTimer(wsPongWait)
		wait:
			for {
				select {
				case <-stop:
					timer.Stop()
					return
				case <-c.closed:
					timer.Stop()
					return
				case <-c.pongCh:
					// Drain pongs the next ping did not ask for.
					select {
					case <-c.pongCh:
					default:
					}
					timer.Stop()
					break wait
				case <-timer.C:
					c.closeSocket(1006)
					return
				}
			}
		}
	}
}
