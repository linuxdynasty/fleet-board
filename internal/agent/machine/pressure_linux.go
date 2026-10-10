package machine

import (
	"context"
	"io"
	"os"
)

// platformPressure is [pressureFromPSI] over the real /proc/pressure/memory.
func platformPressure(_ context.Context, total, available uint64, memOK bool) (uint32, error) {
	return pressureFromPSI(readSmallFile, total, available, memOK)
}

// readSmallFile reads at most psiReadMax bytes of a /proc file.
func readSmallFile(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the one path is the constant psiPath.
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, psiReadMax))
}
