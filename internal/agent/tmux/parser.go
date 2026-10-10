package tmux

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

// The default bounds (defaults.md). The longest notification measured under a
// binary flood was 5,316 bytes (tmux 3.7c; 4,119 on 3.2a); a reply line from
// "capture-pane -J" joins wrapped lines and can be far longer, so one line is
// bounded at 1 MiB and one reply's body at 8 MiB.
const (
	DefaultMaxLineBytes  = 1 << 20
	DefaultMaxReplyBytes = 8 << 20
)

// Options bound a [Parser]. Zero values take the defaults.
type Options struct {
	// MaxLineBytes is the longest line kept, newline excluded. A longer line
	// is skipped to its newline and counted; the parser never holds more
	// than this many bytes of one line.
	MaxLineBytes int
	// MaxReplyBytes bounds the body bytes kept for one reply; the lines from
	// the first that would pass it to the reply's end are dropped and
	// counted in Reply.Dropped.
	MaxReplyBytes int
	// Metrics counts and logs the failures; nil counts nowhere.
	Metrics *Metrics
}

// Parser reads tmux control-mode output (tmux -C) one message at a time. It
// starts no process and writes nothing: it only reads r. A Parser is for one
// goroutine.
type Parser struct {
	r       *bufio.Reader
	maxLine int
	maxBody int
	m       *Metrics
	err     error // the reader's error, returned once the open reply is reported

	open      bool   // a %begin was read and its %end or %error was not
	guardArgs []byte // "time number flags" of the open reply, as tmux wrote them
	reply     Reply
	bodyCost  int  // what the kept body lines cost against maxBody
	readBytes int  // body bytes read for the open reply, kept or not
	overflow  bool // the open reply passed maxBody
}

// NewParser returns a Parser over r with opts' bounds.
func NewParser(r io.Reader, opts Options) *Parser {
	if opts.MaxLineBytes <= 0 {
		opts.MaxLineBytes = DefaultMaxLineBytes
	}
	if opts.MaxReplyBytes <= 0 {
		opts.MaxReplyBytes = DefaultMaxReplyBytes
	}
	return &Parser{
		r:       bufio.NewReaderSize(r, opts.MaxLineBytes+1),
		maxLine: opts.MaxLineBytes,
		maxBody: opts.MaxReplyBytes,
		m:       opts.Metrics,
	}
}

// lineState is what readLine found.
type lineState int

const (
	lineWhole   lineState = iota // a line and its newline
	lineTooLong                  // a line over the bound, skipped to its newline or the end
	linePartial                  // bytes with no newline before the end of the stream
	lineNone                     // nothing before the end of the stream
)

// Next returns the next message. At the end of the stream it returns nil and
// io.EOF (or the reader's error), after a [Discarded] for a reply or line the
// stream cut short.
func (p *Parser) Next() (Message, error) {
	for {
		if p.err != nil {
			if p.open {
				n := p.readBytes
				p.closeReply()
				return p.discard(ErrTruncatedReply, n), nil
			}
			return nil, p.err
		}
		line, state, n, err := p.readLine()
		if err != nil {
			p.err = err
		}
		switch state {
		case lineWhole: // read below
		case lineNone:
			continue
		case lineTooLong:
			p.m.lineTooLong(n, p.maxLine)
			if p.open {
				p.readBytes += n
				p.reply.Dropped++
				continue
			}
			return Discarded{ErrKind: ErrLineTooLong, Bytes: n}, nil
		case linePartial:
			if p.open {
				p.readBytes += n
				continue // reported as the truncated reply
			}
			return p.discard(ErrPartialLine, n), nil
		}
		var m Message
		if p.open {
			m = p.inReply(line)
		} else {
			m = p.outside(line)
		}
		if m != nil {
			return m, nil
		}
	}
}

// readLine returns the next line without its newline. The slice is valid
// until the next read. For a long line, n is its length and its bytes are
// gone; memory stays at the reader's buffer whatever the line's length.
func (p *Parser) readLine() (line []byte, state lineState, n int, err error) {
	b, err := p.r.ReadSlice('\n')
	if err == nil {
		b = b[:len(b)-1]
		if len(b) > p.maxLine { // the reader's buffer is at least 16 bytes
			return nil, lineTooLong, len(b), nil
		}
		return b, lineWhole, len(b), nil
	}
	if !errors.Is(err, bufio.ErrBufferFull) {
		if len(b) == 0 {
			return nil, lineNone, 0, err
		}
		if len(b) > p.maxLine {
			return nil, lineTooLong, len(b), err
		}
		return nil, linePartial, len(b), err
	}
	n = len(b)
	for {
		b, err = p.r.ReadSlice('\n')
		switch {
		case err == nil:
			return nil, lineTooLong, n + len(b) - 1, nil
		case errors.Is(err, bufio.ErrBufferFull):
			n += len(b)
		default:
			return nil, lineTooLong, n + len(b), err
		}
	}
}

// inReply reads one line of an open reply: its closing guard or a body line.
func (p *Parser) inReply(line []byte) Message {
	if args, ok := guardArgs(line, "%end "); ok && bytes.Equal(args, p.guardArgs) {
		return p.finish(false)
	}
	if args, ok := guardArgs(line, "%error "); ok && bytes.Equal(args, p.guardArgs) {
		return p.finish(true)
	}
	// Every other line is body, even one that looks like a notification:
	// command output can be pane text. tmux's %pause and %continue for the
	// client's own refresh-client -A stay here too (Reply.ControlNotes).
	p.readBytes += len(line)
	cost := len(line) + ReplyLineOverhead
	if p.overflow || p.bodyCost+cost > p.maxBody {
		if !p.overflow {
			p.overflow = true
			p.m.parseError(ErrReplyTooLarge, p.readBytes)
		}
		p.reply.Dropped++
		return nil
	}
	p.bodyCost += cost
	p.reply.Body = append(p.reply.Body, string(line))
	return nil
}

// outside reads one line with no reply open: a %begin, which opens one, or a
// notification.
func (p *Parser) outside(line []byte) Message {
	if args, ok := guardArgs(line, "%begin "); ok || bytes.Equal(line, []byte("%begin")) {
		g, valid := parseGuard(args)
		if !valid {
			return p.discard(ErrBadGuard, len(line))
		}
		p.open = true
		p.guardArgs = append(p.guardArgs[:0], args...)
		p.reply = Reply{Time: g.time, Number: g.number, Flags: g.flags}
		return nil
	}
	for _, name := range []string{"%end", "%error"} {
		if args, ok := guardArgs(line, name+" "); ok || bytes.Equal(line, []byte(name)) {
			if _, valid := parseGuard(args); valid {
				return p.discard(ErrUnmatchedGuard, len(line))
			}
			return p.discard(ErrBadGuard, len(line))
		}
	}
	if len(line) == 0 || line[0] != '%' {
		return p.discard(ErrStrayLine, len(line))
	}
	m, kind := parseNotification(line)
	if kind != "" {
		return p.discard(kind, len(line))
	}
	return m
}

func (p *Parser) finish(failed bool) Message {
	r := p.reply
	r.Failed = failed
	p.closeReply()
	return r
}

func (p *Parser) closeReply() {
	p.open, p.overflow, p.bodyCost, p.readBytes = false, false, 0, 0
	p.reply = Reply{}
}

// discard counts a failure and returns it as a message.
func (p *Parser) discard(kind ErrKind, n int) Message {
	p.m.parseError(kind, n)
	return Discarded{ErrKind: kind, Bytes: n}
}

// guardArgs returns what follows prefix in line.
func guardArgs(line []byte, prefix string) ([]byte, bool) {
	if !bytes.HasPrefix(line, []byte(prefix)) {
		return nil, false
	}
	return line[len(prefix):], true
}
