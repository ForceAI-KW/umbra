//go:build !(darwin && arm64)

package vm

// RosettaAvailability is always "notSupported" off darwin/arm64 — Rosetta
// directory shares are a VZ (macOS 13+, Apple Silicon) capability only.
// There is no attachRosetta stub here: config_darwin.go is the only caller,
// and it doesn't compile on this build target.
func RosettaAvailability() string { return "notSupported" }
