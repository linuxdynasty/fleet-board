package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1/agentv1connect"
)

type helloObservation struct {
	hello *agentv1.Hello
	err   error
}

type replayBody struct {
	io.Reader
	io.Closer
}

// captureHello reads just the first framed Connect message and puts its
// bytes back before the real gateway sees the request. It sees no keys or
// later stream messages and does not change the production stream path.
func captureHello(out chan<- helloObservation) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != agentv1connect.AgentServiceConnectProcedure {
				next.ServeHTTP(w, r)
				return
			}
			var captured bytes.Buffer
			tee := io.TeeReader(r.Body, &captured)
			var header [5]byte
			_, err := io.ReadFull(tee, header[:])
			var body []byte
			if err == nil {
				n := binary.BigEndian.Uint32(header[1:])
				if header[0] != 0 || n == 0 || n > 1<<20 {
					err = fmt.Errorf("unexpected Connect frame flags=%d length=%d", header[0], n)
				} else {
					body = make([]byte, n)
					_, err = io.ReadFull(tee, body)
				}
			}
			var msg agentv1.ConnectRequest
			if err == nil {
				err = proto.Unmarshal(body, &msg)
			}
			if err == nil && msg.GetHello() == nil {
				err = errors.New("first Connect frame was not Hello")
			}
			select {
			case out <- helloObservation{hello: msg.GetHello(), err: err}:
			default:
			}
			r.Body = &replayBody{Reader: io.MultiReader(bytes.NewReader(captured.Bytes()), r.Body), Closer: r.Body}
			next.ServeHTTP(w, r)
		})
	}
}
