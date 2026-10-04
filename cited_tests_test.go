package crdt

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// This package ties a claim to the test or benchmark that holds it by NAME --
// in doc comments, in README.md and in docs/design.md. A name is a link with
// nothing checking it: rename the test and the prose still reads as though the
// measurement were there to look at.
//
// It found one. digest.go said "See TestWhatADigestWalkCostsAgainstASnapshot
// for the table", and no such test has ever existed here or in collab: the
// table is the file comment of digest_cost_test.go.
//
// Three forms are legitimate and this knows them apart:
//
//   - a name defined in this module;
//   - a family, written BenchmarkComposite*, where at least one name begins so;
//   - somebody else's, which the prose has to say -- collab's end-to-end tests
//     are cited here and this module cannot resolve them, because it requires
//     nothing and so has no collab source to read. The attribution is the whole
//     check in that case, and saying so is better than resolving nothing
//     quietly.
func TestEveryTestThisPackageCitesExists(t *testing.T) {
	cited := regexp.MustCompile(`\b((?:Test|Fuzz|Benchmark)[A-Z][A-Za-z0-9_]{6,})(\*?)`)
	defined := regexp.MustCompile(`\bfunc ((?:Test|Fuzz|Benchmark)[A-Za-z0-9_]+)\b`)

	here := map[string]bool{}
	type site struct{ file, context, star string }
	mentions := map[string][]site{}
	var read int

	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" || d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		isGo, isMD := strings.HasSuffix(path, ".go"), strings.HasSuffix(path, ".md")
		if !isGo && !isMD {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		read++
		for _, m := range defined.FindAllStringSubmatch(string(body), -1) {
			here[m[1]] = true
		}
		if strings.HasSuffix(path, "_test.go") {
			// A test file naming another test is code, not a claim in prose.
			return nil
		}
		for _, loc := range cited.FindAllStringSubmatchIndex(string(body), -1) {
			name := string(body[loc[2]:loc[3]])
			from := max(loc[2]-40, 0)
			mentions[name] = append(mentions[name], site{
				file:    path,
				context: string(body[from:loc[2]]),
				star:    string(body[loc[4]:loc[5]]),
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// A scan that cannot read reports nothing wrong. A floor, not a count.
	if read < 60 || len(mentions) < 10 {
		t.Fatalf("read %d files and found %d cited names, which is too few to be "+
			"this repository: the scan is broken, not the prose", read, len(mentions))
	}
	t.Logf("%d files read, %d names cited in prose, %d defined here", read, len(mentions), len(here))

	family := func(prefix string) bool {
		for name := range here {
			if strings.HasPrefix(name, prefix) {
				return true
			}
		}
		return false
	}
	for name, sites := range mentions {
		if here[name] {
			continue
		}
		for _, s := range sites {
			switch {
			case s.star == "*" && family(name):
			case strings.Contains(s.context, "collab"):
			default:
				t.Errorf("%s cites %s, which this module does not define, does not "+
					"cite as a family (%s*), and does not say belongs to collab: "+
					"either it was renamed or it never existed", s.file, name, name)
			}
		}
	}
}
