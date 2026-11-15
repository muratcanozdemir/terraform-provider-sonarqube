package provider

import (
	"strings"
	"testing"
)

const testBackup = `<?xml version="1.0" encoding="UTF-8"?><profile><name>Old &amp; name</name><language>py</language><rules>` +
	`<rule><repositoryKey>python</repositoryKey><key>S107</key><type>CODE_SMELL</type><priority>MAJOR</priority>` +
	`<impacts><impact><softwareQuality>MAINTAINABILITY</softwareQuality><severity>MEDIUM</severity></impact></impacts>` +
	`<parameters><parameter><key>max</key><value>9</value></parameter></parameters></rule>` +
	`<rule><repositoryKey>python</repositoryKey><key>S1</key><name>custom</name><priority>MINOR</priority><parameters></parameters></rule>` +
	`</rules></profile>`

func TestParseBackup(t *testing.T) {
	b, err := parseBackup(testBackup)
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "Old & name" || b.Language != "py" || len(b.Rules) != 2 || b.Rules[0].Parameters[0].Value != "9" {
		t.Fatalf("parsed %+v", b)
	}
	for _, bad := range []string{
		"not xml",
		"<other><language>py</language></other>",
		"<profile><name>x</name></profile>",
		"<profile><language>py</language><rules><rule><key>a</key></rule></rules></profile>",
		"<profile><language>py</language><rules><rule><repositoryKey>r</repositoryKey><key>a</key></rule>" +
			"<rule><repositoryKey>r</repositoryKey><key>a</key></rule></rules></profile>",
	} {
		if _, err := parseBackup(bad); err == nil {
			t.Errorf("parseBackup(%q) accepted", bad)
		}
	}
}

func TestWithName(t *testing.T) {
	got, err := withName(testBackup, "New <name>")
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(testBackup, "Old &amp; name", "New &lt;name&gt;", 1); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	// A rule's <name> is not the profile's.
	got, err = withName("<profile><language>py</language><rules><rule><name>r</name></rule></rules></profile>", "p")
	if err != nil || got != "<profile><name>p</name><language>py</language><rules><rule><name>r</name></rule></rules></profile>" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := withName("<profile><name>x</profile>", "p"); err == nil {
		t.Fatal("malformed XML accepted")
	}
}

func TestBackupDiff(t *testing.T) {
	got, _ := parseBackup(testBackup)
	parse := func(rules string) *profileBackup {
		b, err := parseBackup("<profile><language>py</language><rules>" + rules + "</rules></profile>")
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	both := `<rule><repositoryKey>python</repositoryKey><key>S1</key></rule>`
	for name, tc := range map[string]struct {
		rules                   string
		missing, extra, changed int
	}{
		"same, only keys":        {both + `<rule><repositoryKey>python</repositoryKey><key>S107</key></rule>`, 0, 0, 0},
		"same, all fields":       {both + `<rule><repositoryKey>python</repositoryKey><key>S107</key><priority>major</priority><impacts><impact><softwareQuality>MAINTAINABILITY</softwareQuality><severity>MEDIUM</severity></impact></impacts><parameters><parameter><key>max</key><value>9</value></parameter><parameter><key>other</key><value></value></parameter></parameters></rule>`, 0, 0, 0},
		"priority differs":       {both + `<rule><repositoryKey>python</repositoryKey><key>S107</key><priority>BLOCKER</priority></rule>`, 0, 0, 1},
		"impact differs":         {both + `<rule><repositoryKey>python</repositoryKey><key>S107</key><impacts><impact><softwareQuality>MAINTAINABILITY</softwareQuality><severity>HIGH</severity></impact></impacts></rule>`, 0, 0, 1},
		"parameter differs":      {both + `<rule><repositoryKey>python</repositoryKey><key>S107</key><parameters><parameter><key>max</key><value>7</value></parameter></parameters></rule>`, 0, 0, 1},
		"unset parameter is set": {both + `<rule><repositoryKey>python</repositoryKey><key>S107</key><parameters><parameter><key>other</key><value>1</value></parameter></parameters></rule>`, 0, 0, 1},
		"rule missing on server": {both + `<rule><repositoryKey>python</repositoryKey><key>S107</key></rule><rule><repositoryKey>python</repositoryKey><key>S2</key></rule>`, 1, 0, 0},
		"extra rule on server":   {both, 0, 1, 0},
	} {
		m, e, c := backupDiff(parse(tc.rules), got)
		if len(m) != tc.missing || len(e) != tc.extra || len(c) != tc.changed {
			t.Errorf("%s: missing %v, extra %v, changed %v", name, m, e, c)
		}
	}
	if s := diffSummary([]string{"a"}, []string{"1", "2", "3", "4", "5", "6"}, nil); s != "1 not active (a); 6 active but not in the backup (1, 2, 3, 4, 5, ...)" {
		t.Errorf("summary %q", s)
	}
}
