//go:build !darwin && !linux

package wakenet

import (
	"errors"
	"io"
	"time"
)

// fleet-agent runs on macOS and Linux only; elsewhere the package compiles
// and every system source reports that it is unsupported.

var errUnsupported = errors.New("wake and network-change detection: unsupported OS")

// SystemAsleep is unsupported here.
func SystemAsleep() (time.Duration, error) { return 0, errUnsupported }

// SystemReader is unsupported here.
func SystemReader() Reader { return otherReader{} }

type otherReader struct{}

func (otherReader) Read() (Reading, error) { return Reading{}, errUnsupported }

func pushHint([]byte) bool { return false }

func openPush() (io.ReadCloser, error) { return nil, errUnsupported }
