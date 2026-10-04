// Copyright (c) the go-crdt/crdt authors
//
// SPDX-License-Identifier: BSD-3-Clause

package crdt

import (
	"os"
	"strings"
	"testing"
)

// Two decisions in this repository rest on this module requiring nothing:
// docs/performance.md declines to compress the snapshot because "this package
// has no dependencies and a compressor is a dependency", and
// structured/blob.go declines a content-addressed blob store for the same
// reason, in as many words.
//
// Like the determinism claim, this is an absence, so no test that exercises the
// code can hold it: a `require` line added tomorrow leaves every test green and
// quietly makes both of those sentences false.
//
// This is not a rule against ever taking a dependency. It is a rule against
// taking one without noticing that two written decisions stop being true, which
// is why the failure names them.
func TestTheModuleStillRequiresNothing(t *testing.T) {
	gomod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("reading go.mod: %v", err)
	}

	var requires, replaces []string
	block := ""
	for _, line := range strings.Split(string(gomod), "\n") {
		s := strings.TrimSpace(line)
		if i := strings.Index(s, "//"); i >= 0 {
			s = strings.TrimSpace(s[:i])
		}
		switch {
		case s == "require (":
			block = "require"
			continue
		case s == "replace (":
			block = "replace"
			continue
		case s == ")":
			block = ""
			continue
		case strings.HasPrefix(s, "require "):
			requires = append(requires, strings.TrimPrefix(s, "require "))
			continue
		case strings.HasPrefix(s, "replace "):
			replaces = append(replaces, strings.TrimPrefix(s, "replace "))
			continue
		}
		if s == "" {
			continue
		}
		switch block {
		case "require":
			requires = append(requires, s)
		case "replace":
			replaces = append(replaces, s)
		}
	}

	// A parse that found no module line is reading something else, and would
	// report success for the wrong reason.
	if !strings.Contains(string(gomod), "module github.com/go-crdt/crdt") {
		t.Fatal("this is not the module's go.mod; the check is not looking where it thinks")
	}

	for _, r := range requires {
		t.Errorf("go.mod requires %q.\n"+
			"Two written decisions rest on this module requiring nothing, and both stop being true:\n"+
			"  docs/performance.md, on not compressing the snapshot\n"+
			"  structured/blob.go, on not adding a content-addressed blob store\n"+
			"If the dependency is wanted, change them in the same commit rather than leaving them wrong.", r)
	}
	for _, r := range replaces {
		t.Errorf("go.mod has a replace directive (%q), which is a dependency by another name", r)
	}
	t.Logf("go.mod requires nothing, as docs/performance.md and structured/blob.go both say")
}
