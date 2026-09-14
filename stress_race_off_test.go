//go:build !race

package ivy

// raceDetectorOn reports whether the race detector is enabled for this
// build. See stress_race_on_test.go for the rationale.
const raceDetectorOn = false
