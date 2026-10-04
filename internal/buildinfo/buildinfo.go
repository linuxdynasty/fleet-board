package buildinfo

import (
	"fmt"
	"io"
)

// Version is the release version. The linker sets it with -ldflags -X; the
// default marks a build that was not made by a release.
var Version = "dev"

// Program is one binary's identity.
type Program struct {
	Name    string
	Version string
}

// Current returns the identity of the named binary at the linked Version.
func Current(name string) Program {
	return Program{Name: name, Version: Version}
}

// Run is the whole behavior of a scaffold binary: with no arguments or with
// --version it prints "<name> <version>"; -h and --help print the usage; any
// other argument is refused. It returns the process exit code: 0, 2 for a bad
// argument, or 1 when stdout cannot be written (the reason goes to stderr).
func (p Program) Run(args []string, stdout, stderr io.Writer) int {
	usage := fmt.Sprintf("usage: %s [--version]\n", p.Name)
	if len(args) == 0 {
		return p.print(stdout, stderr, fmt.Sprintf("%s %s\n", p.Name, p.Version))
	}
	var text string
	switch args[0] {
	case "--version":
		text = fmt.Sprintf("%s %s\n", p.Name, p.Version)
	case "-h", "--help":
		text = usage
	default:
		return p.refuse(stderr, args[0], usage)
	}
	if len(args) > 1 {
		return p.refuse(stderr, args[1], usage)
	}
	return p.print(stdout, stderr, text)
}

func (p Program) print(stdout, stderr io.Writer, text string) int {
	if _, err := io.WriteString(stdout, text); err != nil {
		// Nothing more can be done if stderr is closed too; the exit code says it.
		_, _ = fmt.Fprintf(stderr, "%s: write: %v\n", p.Name, err)
		return 1
	}
	return 0
}

func (p Program) refuse(stderr io.Writer, arg, usage string) int {
	_, _ = fmt.Fprintf(stderr, "%s: unknown argument %q\n%s", p.Name, arg, usage)
	return 2
}
