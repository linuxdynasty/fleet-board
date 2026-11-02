package logship

import (
	"bytes"
	"fmt"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Framing numbers from defaults.md: flush the open frame every 10 s or
// 256 KiB raw, close it after 5 min or 4 MiB raw. A chunk is one flush: a
// run of blocks, never a whole frame.
const (
	flushInterval = 10 * time.Second
	flushRawBytes = 256 << 10
	frameInterval = 5 * time.Minute
	frameRawBytes = 4 << 20
	maxChunkData  = 960 * 1024
)

// OutChunk is one compressed chunk ready for the spool: the frame's blocks
// since the last flush, decoding to RawLength raw bytes at Offset.
type OutChunk struct {
	Offset         uint64
	Data           []byte
	RawLength      uint32
	FrameStart     bool
	FrameRawOffset uint64
	FirstTime      time.Time
	LastTime       time.Time
}

// Chunker frames one stream's raw bytes into chunks. It keeps one zstd
// frame open and flushes it at the flush point; the frame closes at the
// frame point and the next chunk opens a new one. One goroutine owns a
// Chunker; a crash loses at most the pending (unflushed) bytes.
type Chunker struct {
	buf     bytes.Buffer
	enc     *zstd.Encoder
	started bool

	offset      uint64
	frameOffset uint64
	frameRaw    uint64
	frameSince  time.Time

	pending     uint64
	firstTime   time.Time
	lastTime    time.Time
	lastFlush   time.Time
	initialized bool
}

// NewChunker opens the stream's first frame lazily on the first Write.
func NewChunker() *Chunker { return &Chunker{} }

// Write appends raw bytes and returns the chunks hitting a flush or frame
// point. now is the capture time, monotonic per stream.
func (c *Chunker) Write(raw []byte, now time.Time) ([]OutChunk, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if err := c.open(now); err != nil {
		return nil, err
	}
	var out []OutChunk
	for len(raw) > 0 {
		// Reopen after a closing flush: the next bytes start a new frame.
		if err := c.open(now); err != nil {
			return nil, err
		}
		// The frame closes before bytes that would pass it: the chunk
		// carrying them opens the next frame.
		if c.frameRaw >= frameRawBytes || now.Sub(c.frameSince) >= frameInterval {
			ch, err := c.flush(now, true)
			if err != nil {
				return nil, err
			}
			out = append(out, ch...)
			continue
		}
		n := len(raw)
		if uint64(n) > flushRawBytes-c.pending {
			n = int(flushRawBytes - c.pending)
		}
		if n <= 0 {
			ch, err := c.flush(now, false)
			if err != nil {
				return nil, err
			}
			out = append(out, ch...)
			continue
		}
		if err := c.append(raw[:n], now); err != nil {
			return nil, err
		}
		raw = raw[n:]
		if c.pending >= flushRawBytes {
			ch, err := c.flush(now, false)
			if err != nil {
				return nil, err
			}
			out = append(out, ch...)
		}
	}
	// Time alone flushes pending bytes: a quiet stream still ships every
	// 10 s once the tailer polls.
	if c.pending > 0 && now.Sub(c.lastFlush) >= flushInterval {
		ch, err := c.flush(now, false)
		if err != nil {
			return nil, err
		}
		out = append(out, ch...)
	}
	return out, nil
}

// Flush ships pending bytes when the 10 s point passed. The tailer calls
// it on its poll so quiet streams still ship.
func (c *Chunker) Flush(now time.Time) ([]OutChunk, error) {
	if c.pending == 0 || now.Sub(c.lastFlush) < flushInterval {
		return nil, nil
	}
	return c.flush(now, false)
}

// Finalize flushes the rest and closes the frame: the stream ended. A
// stream that ends with nothing pending still emits the frame epilogue,
// so the frame decodes whole from its chunks.
func (c *Chunker) Finalize(now time.Time) ([]OutChunk, error) {
	if c.pending == 0 && !c.started {
		return nil, nil
	}
	return c.flush(now, true)
}

// Offset is the stream's raw end: the next byte's offset.
func (c *Chunker) Offset() uint64 { return c.offset }

// open starts the encoder on first use.
func (c *Chunker) open(now time.Time) error {
	if c.started {
		return nil
	}
	c.buf.Reset()
	enc, err := zstd.NewWriter(&c.buf)
	if err != nil {
		return fmt.Errorf("logship: frame encoder: %w", err)
	}
	c.enc = enc
	c.started = true
	c.frameOffset = c.offset
	c.frameRaw = 0
	c.frameSince = now
	c.pending = 0
	c.firstTime = now
	return nil
}

func (c *Chunker) append(raw []byte, now time.Time) error {
	if !c.initialized {
		c.firstTime = now
		c.lastFlush = now
		c.initialized = true
	}
	if c.pending == 0 {
		c.firstTime = now
	}
	if _, err := c.enc.Write(raw); err != nil {
		return fmt.Errorf("logship: frame write: %w", err)
	}
	c.pending += uint64(len(raw))
	c.lastTime = now
	c.offset += uint64(len(raw))
	c.frameRaw += uint64(len(raw))
	return nil
}

// flush ships pending bytes as one chunk, closing the frame when asked.
// Close's epilogue lands in the same chunk, so a closed frame decodes
// whole from its chunks.
func (c *Chunker) flush(now time.Time, closeFrame bool) ([]OutChunk, error) {
	if err := c.enc.Flush(); err != nil {
		return nil, fmt.Errorf("logship: frame flush: %w", err)
	}
	data := append([]byte{}, c.buf.Bytes()...)
	c.buf.Reset()
	if closeFrame {
		if err := c.enc.Close(); err != nil {
			return nil, fmt.Errorf("logship: close frame: %w", err)
		}
		data = append(data, c.buf.Bytes()...)
		c.buf.Reset()
		c.started = false
	}
	ch := OutChunk{
		Offset: c.offset - c.pending,
		Data:   data,
		// Pending never exceeds the 256 KiB flush point, far below 4 GiB.
		RawLength:      uint32(c.pending), //nolint:gosec // G115: pending never exceeds the 256 KiB flush point
		FrameStart:     c.frameOffset == c.offset-c.pending,
		FrameRawOffset: c.frameOffset,
		FirstTime:      c.firstTime,
		LastTime:       now,
	}
	if len(data) > maxChunkData {
		return nil, fmt.Errorf("logship: chunk %d bytes is above %d", len(data), maxChunkData)
	}
	c.pending = 0
	c.lastFlush = now
	// Even a zero-length closing chunk is kept: it carries the frame
	// epilogue the earlier chunks of the frame decode with.
	return []OutChunk{ch}, nil
}
