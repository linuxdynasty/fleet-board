package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChooseRepos(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	repos := filepath.Join(home, "repos")
	if err := os.Mkdir(repos, 0o755); err != nil { //nolint:gosec // G301: a test folder
		t.Fatal(err)
	}
	cases := []struct {
		name        string
		flag        string
		interactive bool
		input       string
		want        string
		wantErr     bool
		asked       int // how many times the prompt was printed
		said        string
	}{
		{name: "the flag wins and nothing is asked", flag: "~/repos", interactive: true, input: "ignored\n", want: repos},
		{name: "a bad flag is an error", flag: "~/nope", wantErr: true},
		{name: "no terminal and no flag", input: repos + "\n", want: ""},
		{name: "a terminal answer", interactive: true, input: "~/repos\n", want: repos, asked: 1},
		{name: "a bad answer asks again", interactive: true, input: "~/nope\n" + repos + "\n", want: repos, asked: 2, said: "not an existing folder"},
		{name: "Enter skips", interactive: true, input: "\n", want: "", asked: 1},
		{name: "end of input skips", interactive: true, input: "", want: "", asked: 1},
		{name: "an answer with no newline at the end", interactive: true, input: repos, want: repos, asked: 1},
		{name: "a bad last line with no newline skips", interactive: true, input: "nope", want: "", asked: 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			got, err := chooseRepos(c.flag, c.interactive, strings.NewReader(c.input), &out, home)
			if (err != nil) != c.wantErr || got != c.want {
				t.Errorf("chooseRepos = %q, %v; want %q, error %v", got, err, c.want, c.wantErr)
			}
			if n := strings.Count(out.String(), "Folder that holds your repos (for example ~/repos):"); n != c.asked {
				t.Errorf("prompt printed %d times, want %d:\n%s", n, c.asked, out.String())
			}
			if !strings.Contains(out.String(), c.said) {
				t.Errorf("output %q does not say %q", out.String(), c.said)
			}
		})
	}
}

func TestIsTerminalIsFalseForPipesFilesAndDevNull(t *testing.T) {
	t.Parallel()
	if isTerminal(strings.NewReader("x")) {
		t.Error("a string reader is not a terminal")
	}
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if isTerminal(f) {
		t.Error("/dev/null is not a terminal")
	}
	file, err := os.Open(filepath.Join(t.TempDir(), ".."))
	if err == nil {
		defer func() { _ = file.Close() }()
		if isTerminal(file) {
			t.Error("a folder is not a terminal")
		}
	}
}
