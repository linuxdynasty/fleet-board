package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1/agentv1connect"
)

type missingChildWatcher struct{ adds, closes atomic.Int32 }

func (w *missingChildWatcher) Add(dir string) error {
	w.adds.Add(1)
	return fmt.Errorf("watch child: %w", &os.PathError{Op: "lstat", Path: filepath.Join(dir, ".policy.tmp"), Err: os.ErrNotExist})
}
func (*missingChildWatcher) Remove(string) error           { return nil }
func (*missingChildWatcher) Events() <-chan fsnotify.Event { return nil }
func (*missingChildWatcher) Errors() <-chan error          { return nil }
func (w *missingChildWatcher) Close() error                { w.closes.Add(1); return nil }

type policyWireSnapshot struct {
	snapshot *agentv1.Snapshot
	err      error
}

// Observe only the first Snapshot per connection. The real handler receives
// the original bytes; later events and Settings keep their production path.
func captureFirstPolicySnapshot(out chan<- policyWireSnapshot) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == agentv1connect.AgentServiceConnectProcedure {
				r.Body = &policySnapshotBody{ReadCloser: r.Body, out: out}
			}
			next.ServeHTTP(w, r)
		})
	}
}

type policySnapshotBody struct {
	io.ReadCloser
	out  chan<- policyWireSnapshot
	buf  []byte
	done bool
}

func (b *policySnapshotBody) report(observation policyWireSnapshot) {
	b.done = true
	b.buf = nil
	select {
	case b.out <- observation:
	default:
	}
}

func (b *policySnapshotBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if b.done {
		return n, err
	}
	if len(b.buf)+n > 2<<20 {
		b.report(policyWireSnapshot{err: fmt.Errorf("policy Snapshot observation exceeded its byte bound")})
		return n, err
	}
	b.buf = append(b.buf, p[:n]...)
	for len(b.buf) >= 5 {
		size := binary.BigEndian.Uint32(b.buf[1:5])
		if b.buf[0] != 0 || size > 1<<20 {
			b.report(policyWireSnapshot{err: fmt.Errorf("unexpected policy Snapshot frame flags=%d length=%d", b.buf[0], size)})
			break
		}
		if len(b.buf) < 5+int(size) {
			break
		}
		var message agentv1.ConnectRequest
		if decodeErr := proto.Unmarshal(b.buf[5:5+size], &message); decodeErr != nil {
			b.report(policyWireSnapshot{err: decodeErr})
			break
		}
		if snapshot := message.GetSnapshot(); snapshot != nil {
			b.report(policyWireSnapshot{snapshot: snapshot})
			break
		}
		b.buf = b.buf[5+size:]
	}
	return n, err
}

func nextPolicySnapshot(t *testing.T, snapshots <-chan policyWireSnapshot) *agentv1.Snapshot {
	t.Helper()
	select {
	case observed := <-snapshots:
		if observed.err != nil || observed.snapshot == nil {
			t.Fatalf("policy Snapshot observation: %v", observed.err)
		}
		return observed.snapshot
	case <-time.After(20 * time.Second):
		t.Fatal("no first policy Snapshot received")
		return nil
	}
}
