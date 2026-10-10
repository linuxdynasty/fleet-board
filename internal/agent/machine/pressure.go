package machine

import (
	"fmt"
	"math"
	"math/bits"
	"strconv"
	"strings"
)

// The memory pressure levels are the ones macOS reports in
// kern.memorystatus_vm_pressure_level and ResourceSample.mem_pressure_level
// names: 1 normal, 2 warn, 4 critical. Linux has no such number, so the agent
// derives one (defaults.md "Memory pressure level"; decided in plan row 6):
//
//   - from the kernel's pressure stall information (PSI), the share of the
//     last 10 s in which some task stalled for memory: under 10 normal,
//     10 to under 40 warn, 40 or more critical;
//   - when PSI is not available (kernel before 4.20, PSI off, cgroup v1
//     without it), from MemAvailable: 15 percent of total or more normal,
//     5 to under 15 warn, under 5 critical.
//
// A host with neither reading has no level (unset, never "normal").
const (
	levelNormal   uint32 = 1
	levelWarn     uint32 = 2
	levelCritical uint32 = 4

	psiWarnAt     = 10.0
	psiCriticalAt = 40.0

	availWarnPercent     = 15
	availCriticalPercent = 5

	// psiPath is the kernel's memory PSI file; psiReadMax bounds a read of it.
	psiPath    = "/proc/pressure/memory"
	psiReadMax = 4 << 10
)

// levelFromPSI maps the `some avg10` percentage to a level.
func levelFromPSI(avg10 float64) uint32 {
	switch {
	case avg10 >= psiCriticalAt:
		return levelCritical
	case avg10 >= psiWarnAt:
		return levelWarn
	default:
		return levelNormal
	}
}

// levelFromAvailable maps MemAvailable against the total to a level. A total
// of zero, or an available figure above it, is not a reading.
func levelFromAvailable(total, available uint64) (uint32, error) {
	if total == 0 || available > total {
		return 0, fmt.Errorf("%w: memory total %d, available %d", errParse, total, available)
	}
	switch {
	case percentBelow(available, total, availCriticalPercent):
		return levelCritical, nil
	case percentBelow(available, total, availWarnPercent):
		return levelWarn, nil
	default:
		return levelNormal, nil
	}
}

// percentBelow reports available*100 < total*p, exactly for any uint64: the
// products are compared as 128-bit numbers.
func percentBelow(available, total, p uint64) bool {
	aHi, aLo := bits.Mul64(available, 100)
	tHi, tLo := bits.Mul64(total, p)
	return aHi < tHi || (aHi == tHi && aLo < tLo)
}

// parsePSI returns `avg10` of the `some` line of a PSI file.
func parsePSI(text string) (float64, error) {
	for line := range strings.SplitSeq(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "some" {
			continue
		}
		for _, f := range fields[1:] {
			v, ok := strings.CutPrefix(f, "avg10=")
			if !ok {
				continue
			}
			n, err := strconv.ParseFloat(v, 64)
			if err != nil || math.IsNaN(n) || n < 0 || n > 100 {
				return 0, fmt.Errorf("%w: PSI avg10", errParse)
			}
			return n, nil
		}
	}
	return 0, fmt.Errorf("%w: no PSI some avg10", errParse)
}

// pressureFromPSI is the Linux reading: PSI first, MemAvailable when PSI
// cannot be read or understood. readFile reads psiPath (injected for tests).
func pressureFromPSI(readFile func(path string) ([]byte, error), total, available uint64, memOK bool) (uint32, error) {
	if b, err := readFile(psiPath); err == nil {
		if avg, err := parsePSI(string(b)); err == nil {
			return levelFromPSI(avg), nil
		}
	}
	if !memOK {
		return 0, fmt.Errorf("%w: no PSI and no memory reading", errParse)
	}
	return levelFromAvailable(total, available)
}
