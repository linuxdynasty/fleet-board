package daemon_test

import (
	"net/http"
	"sync"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/daemon"
	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
)

// The agent listener is served and stopped by the *gateway.Server itself,
// not its embedded *http.Server: only the gateway's Shutdown waits for the
// Connect handlers to close their rows before the store closes (03n
// "graceful shutdown"). Both satisfy the same interface, so the compiler
// cannot tell them apart.
func TestRunServesTheAgentListenerWithTheGatewayServer(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	var mu sync.Mutex
	srvs := map[string]any{}
	opts := h.options()
	daemon.SetAfterBind(&opts, func(name string, srv any) {
		mu.Lock()
		defer mu.Unlock()
		srvs[name] = srv
	})
	r := h.start(t, opts)
	r.stop(t)
	mu.Lock()
	defer mu.Unlock()
	if _, ok := srvs["agent"].(*gateway.Server); !ok {
		t.Errorf("agent listener served by %T, want *gateway.Server", srvs["agent"])
	}
	if _, ok := srvs["api"].(*http.Server); !ok {
		t.Errorf("api listener served by %T, want *http.Server", srvs["api"])
	}
}
