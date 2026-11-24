package provider

import (
	"bufio"
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestSelectionMap checks scripts/acc-test-map.txt, which selects the
// acceptance tests of a pull request: every test name it lists must be the
// prefix of an acceptance test, so a renamed test cannot silently drop out
// of the selection.
func TestSelectionMap(t *testing.T) {
	defined := map[string]bool{}
	files, _ := os.ReadDir(".")
	re := regexp.MustCompile(`(?m)^func (TestAcc\w+)\(t \*testing\.T\)`)
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			defined[m[1]] = true
		}
	}

	f, err := os.Open("../../scripts/acc-test-map.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if len(fields) != 2 {
			t.Errorf("line %d: want `<glob> <selection>`, got %q", n, sc.Text())
			continue
		}
		switch sel := fields[1]; sel {
		case "ALL", "NONE", "MIGRATION":
		default:
			for name := range strings.SplitSeq(sel, "|") {
				found := false
				for d := range defined {
					found = found || strings.HasPrefix(d, name)
				}
				if !found {
					t.Errorf("line %d: %q matches no acceptance test", n, name)
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
}
