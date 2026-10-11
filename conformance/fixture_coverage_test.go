package conformance

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The drift check (.github/workflows/conformance-drift-check.yml) guards every vendored fixture's
// BYTES against canonical. Nothing guarded that a runner ever OPENS one - so a fixture could be
// vendored, kept perfectly in sync forever, and never executed, which is indistinguishable from a
// passing claim when you only look at CI. Two fixtures were in exactly that state.
//
// This test closes that half: every file in testdata/ is either loaded by a runner or listed in
// fixturesNotRun with a reason. Both halves are checked, so an opt-out cannot outlive the gap it
// was written for.

// runnerDirs are the directories holding conformance runners that read testdata/. Runners live in
// two modules - the root module's runner here, and codegen's, which reaches the shared testdata
// directory by relative path rather than by importing the root module.
var runnerDirs = []string{
	".",
	filepath.Join("..", "codegen", "conformance"),
}

// fixturesNotRun lists vendored fixtures no runner executes, each with the reason. An entry here
// is a debt, not a dismissal: it is a fixture this port carries and does not honour, and it should
// leave this list by being run, not by being deleted.
var fixturesNotRun = map[string]string{
	// The Service Versions module (service-versions.md §1 identity, §2 order; Provisional). Both
	// fixtures are collector-conditional - a collector without the module stays
	// collector-conformant - and meshd does not implement it: it keys its catalog by service, and
	// compares a heartbeat's registrationHash with the service's one registration (mesh.md §5).
	// These entries go when meshd implements the module (a runner is added) or the fixtures stop
	// being vendored.
	"mesh-service-version-cases.json": "service-versions §1 identity: meshd keys by service; the module is not implemented",
	"mesh-version-order-cases.json":   "service-versions §2 order: meshd keys by service; the module is not implemented",
}

func TestConformance_EveryFixtureIsRun(t *testing.T) {
	fixtures, err := filepath.Glob(filepath.Join("testdata", "*.json"))
	if err != nil {
		t.Fatalf("glob testdata: %v", err)
	}
	requireCases(t, len(fixtures), "testdata", "*.json")

	sources := runnerSources(t)

	present := make(map[string]bool, len(fixtures))
	for _, path := range fixtures {
		name := filepath.Base(path)
		present[name] = true

		// A quoted literal, not a bare mention: the runners name fixtures in prose comments too
		// ("--- envelope-cases.json ---"), and a comment runs nothing. Quotes are what tells a
		// filename being opened from a filename being talked about, without tying this check to
		// one particular loader helper.
		loaded := false
		for _, src := range sources {
			if strings.Contains(src, strconv.Quote(name)) {
				loaded = true
				break
			}
		}

		reason, excused := fixturesNotRun[name]
		switch {
		case loaded && excused:
			t.Errorf("%s is listed in fixturesNotRun (%q) but a runner does load it - drop the entry", name, reason)
		case !loaded && !excused:
			t.Errorf("%s is vendored but no runner loads it: write a runner, or list it in fixturesNotRun with the reason it is not run", name)
		}
	}

	for name := range fixturesNotRun {
		if !present[name] {
			t.Errorf("fixturesNotRun names %s, which is not vendored in testdata/ - drop the entry", name)
		}
	}
}

// runnerSources reads the Go source of every runner directory. Reading the source, rather than
// asking the runners at runtime, is what lets this notice a fixture nothing even mentions - the
// exact case that hid.
func runnerSources(t *testing.T) []string {
	t.Helper()

	var sources []string
	for _, dir := range runnerDirs {
		matches, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatalf("glob %s: %v", dir, err)
		}
		if len(matches) == 0 {
			t.Fatalf("runner directory %s holds no Go source - has a runner moved?", dir)
		}
		for _, path := range matches {
			if filepath.Base(path) == "fixture_coverage_test.go" {
				continue // this file names the opted-out fixtures, which would excuse them from itself
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			sources = append(sources, string(data))
		}
	}
	return sources
}
