package attach

import (
	"errors"

	"github.com/creack/pty"
	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// outReadSize bounds one output read: well under the hub's 64 KiB chunk,
// so one read is one TermUp with no re-chunking.
const outReadSize = 32768

// outPump renders the PTY to the hub: stripped, never dropped. A slow hub
// holds this pump in Send (HTTP/2 flow control), which holds the PTY, but
// never another attach. PTY death ends the attach as ERROR; a dead stream
// ends it silently, the hub already knows.
func (m *Manager) outPump(st *attach) {
	defer m.wg.Done()
	buf := make([]byte, outReadSize)
	id := &typev1.AttachId{Value: st.id}
	for {
		n, err := st.client.Master.Read(buf)
		if n > 0 {
			if clean := st.strip.Strip(buf[:n]); len(clean) > 0 {
				req := &agentv1.TerminalRequest{Up: &agentv1.TermUp{
					AttachId: id,
					Body:     &agentv1.TermUp_Output{Output: clean},
				}}
				if serr := st.stream.Send(req); serr != nil {
					m.finish(st.id, typev1.AttachCloseReason_ATTACH_CLOSE_REASON_UNSPECIFIED, false)
					return
				}
			}
		}
		if err != nil {
			m.finish(st.id, typev1.AttachCloseReason_ATTACH_CLOSE_REASON_ERROR, true)
			return
		}
	}
}

// inPump carries the hub into the PTY: input (dropped for viewers),
// resize, window and pane selects, and close. A select that fails (a stale
// key, a dying server) only skips that navigation: the attach lives on.
// Stream death ends the attach silently. The receive runs on its own
// goroutine so teardown (st.ctx) ends this pump even when the stream is
// wedged and never answers: the teardown forgets the id and kills the
// PTY and view, and the receiver exits with the stream.
func (m *Manager) inPump(st *attach) {
	defer m.wg.Done()
	type outcome struct {
		resp *agentv1.TerminalResponse
		err  error
	}
	downs := make(chan outcome)
	go func() {
		for {
			resp, err := st.stream.Receive()
			select {
			case downs <- outcome{resp: resp, err: err}:
			case <-st.ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		select {
		case <-st.ctx.Done():
			m.finish(st.id, typev1.AttachCloseReason_ATTACH_CLOSE_REASON_UNSPECIFIED, false)
			return
		case o := <-downs:
			if o.err != nil {
				m.finish(st.id, typev1.AttachCloseReason_ATTACH_CLOSE_REASON_UNSPECIFIED, false)
				return
			}
			if m.handleDown(st, o.resp) {
				return
			}
		}
	}
}

// handleDown acts on one hub message; true ends the attach.
func (m *Manager) handleDown(st *attach, resp *agentv1.TerminalResponse) bool {
	down := resp.GetDown()
	if down == nil || down.GetAttachId().GetValue() != st.id {
		return false
	}
	switch body := down.GetBody().(type) {
	case *agentv1.TermDown_Input:
		if len(body.Input) == 0 || st.readOnly {
			return false
		}
		if err := writeFull(st.client.Master, body.Input); err != nil {
			m.finish(st.id, typev1.AttachCloseReason_ATTACH_CLOSE_REASON_ERROR, true)
			return true
		}
	case *agentv1.TermDown_Resize:
		cols, rows := int(body.Resize.GetCols()), int(body.Resize.GetRows())
		if !validDims(cols, rows) {
			m.log.Debug("attach.resize_invalid", "attach_id", st.id, "cols", cols, "rows", rows)
			return false
		}
		// A dying client errors here; the out pump reports it.
		// Cells fit: validDims bounds both to 1..1000.
		rows16 := uint16(rows) //nolint:gosec // G115: as above
		cols16 := uint16(cols) //nolint:gosec // G115: as above
		_ = pty.Setsize(st.client.Master, &pty.Winsize{Rows: rows16, Cols: cols16})
	case *agentv1.TermDown_Select:
		m.selectOn(st, body.Select)
	case *agentv1.TermDown_Close:
		m.finish(st.id, typev1.AttachCloseReason_ATTACH_CLOSE_REASON_DETACHED, true)
		return true
	}
	return false
}

// selectOn moves the view's current window or active pane. Only the
// attach's own server is selectable; a key from another server or a stale
// generation is refused before it can land anywhere.
func (m *Manager) selectOn(st *attach, sel *agentv1.TermSelect) {
	switch sel.GetTarget().(type) {
	case *agentv1.TermSelect_Window:
		w := sel.GetWindow()
		if w.GetServer() != st.server {
			m.log.Debug("attach.select_server", "attach_id", st.id, "server", w.GetServer())
			return
		}
		if err := m.views.SelectWindow(st.ctx, st.socket, w.GetServer(), w.GetServerStartId(), st.view, w.GetWindowId()); err != nil {
			m.log.Debug("attach.select_window", "attach_id", st.id, "err", err)
		}
	case *agentv1.TermSelect_Pane:
		p := sel.GetPane()
		if p.GetServer() != st.server {
			m.log.Debug("attach.select_server", "attach_id", st.id, "server", p.GetServer())
			return
		}
		if err := m.views.SelectPane(st.ctx, st.socket, p.GetServer(), p.GetServerStartId(), st.view, p.GetPaneId()); err != nil {
			m.log.Debug("attach.select_pane", "attach_id", st.id, "err", err)
		}
	default:
		m.log.Debug("attach.select_unknown", "attach_id", st.id)
	}
}

func writeFull(f interface{ Write([]byte) (int, error) }, data []byte) error {
	for len(data) > 0 {
		n, err := f.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 {
			return errors.New("attach: short write to the client")
		}
		data = data[n:]
	}
	return nil
}
