package main

import (
	"os"

	"github.com/linuxdynasty/fleet-board/internal/buildinfo"
)

func main() {
	os.Exit(buildinfo.Current("fleet").Run(os.Args[1:], os.Stdout, os.Stderr))
}
