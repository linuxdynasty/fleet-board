//go:build race

package scrub_test

// raceDetector is true under -race, which slows the regexp engine about
// twentyfold, too much for a wall-time budget.
const raceDetector = true
