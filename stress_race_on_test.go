//go:build race

package ivy

// raceDetectorOn reports whether the race detector is enabled for this
// build. It gates tests that exercise known-open data races (issue #47
// family) whose FUNCTIONAL behavior is still worth checking in plain
// `go test ./...` runs: the race detector would fail the whole CI job on
// those unsynchronized writes, so under -race we skip and point at the
// issue instead.
const raceDetectorOn = true
