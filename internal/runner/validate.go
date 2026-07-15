package runner

import "regexp"

// validRepoPattern matches "org/repo" style refs: letters, digits, dot,
// underscore, dash on each side of exactly one slash.
//
// This is the real gate against shell injection: InstallScript
// fmt.Sprintf's RepoURL, RunnerName, Labels, and DirName unescaped into
// double-quoted bash contexts (see script.go). A value containing '"', a
// backtick, or "$(...)" would break out of those quotes and execute as the
// umbra guest user. The caller (cmd/umbra/runner.go) MUST validate every
// flag that ends up in an InstallParams with ValidRepo/ValidRunnerField
// before calling InstallScript. InstallScript's own quoting is kept as
// defense in depth only — it is not sufficient on its own.
var validRepoPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)

// validRunnerFieldPattern matches runner-name / labels / directory-name
// values: letters, digits, comma (labels are comma-separated), dot,
// underscore, dash. Same shell-injection rationale as validRepoPattern.
var validRunnerFieldPattern = regexp.MustCompile(`^[A-Za-z0-9,_.-]+$`)

// ValidRepo reports whether s is safe to interpolate into InstallScript as
// RepoURL's "org/repo" portion. Exported so cmd/umbra/runner.go can gate
// --repo before building InstallParams.
func ValidRepo(s string) bool {
	return validRepoPattern.MatchString(s)
}

// ValidRunnerField reports whether s is safe to interpolate into
// InstallScript as RunnerName, Labels, or DirName. Exported so
// cmd/umbra/runner.go can gate --name/--labels/derived directory names
// before building InstallParams.
func ValidRunnerField(s string) bool {
	return validRunnerFieldPattern.MatchString(s)
}
