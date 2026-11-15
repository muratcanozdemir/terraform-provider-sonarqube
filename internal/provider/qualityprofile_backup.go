package provider

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// profileBackup is the part of a quality profile backup (the XML of
// api/qualityprofiles/backup and restore) that the provider compares.
type profileBackup struct {
	XMLName  xml.Name     `xml:"profile"`
	Name     string       `xml:"name"`
	Language string       `xml:"language"`
	Rules    []backupRule `xml:"rules>rule"`
}

type backupRule struct {
	RepositoryKey string         `xml:"repositoryKey"`
	Key           string         `xml:"key"`
	Priority      string         `xml:"priority"`
	Impacts       []backupImpact `xml:"impacts>impact"`
	Parameters    []backupParam  `xml:"parameters>parameter"`
}

type backupImpact struct {
	SoftwareQuality string `xml:"softwareQuality"`
	Severity        string `xml:"severity"`
}

type backupParam struct {
	Key   string `xml:"key"`
	Value string `xml:"value"`
}

func (r *backupRule) id() string { return r.RepositoryKey + ":" + r.Key }

// parseBackup parses a profile backup and checks that every rule is named
// once.
func parseBackup(s string) (*profileBackup, error) {
	var b profileBackup
	if err := xml.Unmarshal([]byte(s), &b); err != nil {
		return nil, fmt.Errorf("not a quality profile backup: %w", err)
	}
	if b.Language == "" {
		return nil, errors.New("the backup has no <language>")
	}
	seen := map[string]bool{}
	for i := range b.Rules {
		r := &b.Rules[i]
		if r.RepositoryKey == "" || r.Key == "" {
			return nil, fmt.Errorf("rule %d of the backup has no <repositoryKey> or <key>", i+1)
		}
		if seen[r.id()] {
			return nil, fmt.Errorf("the backup lists rule %s more than once", r.id())
		}
		seen[r.id()] = true
	}
	return &b, nil
}

// withName returns the backup with the profile's <name> set to name.
// SonarQube restores a backup into the profile named in it, so the
// resource's name decides which profile is written. The rest of the
// document is kept byte for byte.
func withName(s, name string) (string, error) {
	var esc bytes.Buffer
	if err := xml.EscapeText(&esc, []byte(name)); err != nil {
		return "", err
	}
	d := xml.NewDecoder(strings.NewReader(s))
	depth, start := 0, int64(-1)
	for {
		prev := d.InputOffset()
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("not a quality profile backup: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if depth == 2 && t.Name.Local == "name" {
				start = d.InputOffset()
			}
		case xml.EndElement:
			if depth == 2 && t.Name.Local == "name" && start >= 0 {
				return s[:start] + esc.String() + s[prev:], nil
			}
			depth--
		}
	}
	// No <name>: add one as the profile's first child.
	i := strings.Index(s, "<profile>")
	if i < 0 {
		return "", errors.New("the backup has no <profile> element")
	}
	i += len("<profile>")
	return s[:i] + "<name>" + esc.String() + "</name>" + s[i:], nil
}

// backupDiff lists how the server's profile (got) differs from a configured
// backup (want). Only what want specifies is compared: a rule's priority,
// impacts and parameters count only when present in want, so a backup
// written by hand needs to list just the rules and what it sets. Rules
// active on the server but missing from want are differences too, because
// restoring the backup deactivates them.
func backupDiff(want, got *profileBackup) (missing, extra, changed []string) {
	have := map[string]*backupRule{}
	for i := range got.Rules {
		have[got.Rules[i].id()] = &got.Rules[i]
	}
	wanted := map[string]bool{}
	for i := range want.Rules {
		w := &want.Rules[i]
		wanted[w.id()] = true
		g := have[w.id()]
		if g == nil {
			missing = append(missing, w.id())
			continue
		}
		if !ruleMatches(w, g) {
			changed = append(changed, w.id())
		}
	}
	for id := range have {
		if !wanted[id] {
			extra = append(extra, id)
		}
	}
	slices.Sort(missing)
	slices.Sort(extra)
	slices.Sort(changed)
	return missing, extra, changed
}

func ruleMatches(want, got *backupRule) bool {
	if want.Priority != "" && !strings.EqualFold(want.Priority, got.Priority) {
		return false
	}
	for _, wi := range want.Impacts {
		if !slices.ContainsFunc(got.Impacts, func(gi backupImpact) bool {
			return strings.EqualFold(gi.SoftwareQuality, wi.SoftwareQuality) && strings.EqualFold(gi.Severity, wi.Severity)
		}) {
			return false
		}
	}
	for _, wp := range want.Parameters {
		found := false
		for _, gp := range got.Parameters {
			if gp.Key == wp.Key {
				found = gp.Value == wp.Value
				break
			}
		}
		// An empty value means the rule's default, which the server may
		// leave out of the backup.
		if !found && wp.Value != "" {
			return false
		}
	}
	return true
}

// diffSummary describes a backupDiff for a diagnostic, naming up to a few
// rules of each kind.
func diffSummary(missing, extra, changed []string) string {
	var parts []string
	add := func(what string, ids []string) {
		if len(ids) == 0 {
			return
		}
		shown := ids
		if len(shown) > 5 {
			shown = shown[:5]
		}
		s := fmt.Sprintf("%d %s (%s", len(ids), what, strings.Join(shown, ", "))
		if len(ids) > len(shown) {
			s += ", ..."
		}
		parts = append(parts, s+")")
	}
	add("not active", missing)
	add("active but not in the backup", extra)
	add("with other settings", changed)
	return strings.Join(parts, "; ")
}
