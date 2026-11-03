package attach

import (
	"os"
	"slices"
	"testing"
)

// The attach client dies at once without a capable TERM, so OpenClient
// defaults it: a missing, empty, dumb or unknown TERM becomes
// xterm-256color, and anything else is inherited untouched.
func TestClientEnvDefaultsUnusableTerm(t *testing.T) {
	// Not parallel: t.Setenv cannot run beside t.Parallel.
	for _, term := range []string{"", "dumb", "unknown"} {
		t.Setenv("TERM", term)
		env := clientEnv()
		if env == nil {
			t.Fatalf("TERM=%q: clientEnv is nil, want a defaulted environment", term)
		}
		if !slices.Contains(env, "TERM=xterm-256color") {
			t.Fatalf("TERM=%q: environment %q lacks the default TERM", term, env)
		}
		if slices.Contains(env, "TERM="+term) {
			t.Fatalf("TERM=%q: unusable TERM leaks into the client environment", term)
		}
	}
}

func TestClientEnvUnsetTermDefaults(t *testing.T) {
	// Not parallel: it mutates the process environment.
	old, had := os.LookupEnv("TERM")
	if err := os.Unsetenv("TERM"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("TERM", old)
		}
	})
	env := clientEnv()
	if env == nil {
		t.Fatal("unset TERM: clientEnv is nil, want a defaulted environment")
	}
	if !slices.Contains(env, "TERM=xterm-256color") {
		t.Fatalf("environment %q lacks TERM=xterm-256color", env)
	}
}

func TestClientEnvKeepsCapableTerm(t *testing.T) {
	// Not parallel: t.Setenv cannot run beside t.Parallel.
	t.Setenv("TERM", "tmux-256color")
	if env := clientEnv(); env != nil {
		t.Fatalf("TERM=tmux-256color: clientEnv = %q, want nil (inherit)", env)
	}
}
