package web

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path"
	"regexp"
	"strings"
	"testing"
)

// VENDOR.md is the lockfile for static/'s third-party files: with zero NPM
// dependencies there is no manifest, no integrity field, and nothing but this
// test to notice that a hand-vendored drop-in is truncated, tampered with, or a
// different release than the record claims. It runs inside the Docker build
// (which copies web/, .dockerignore strips only the root README), so a bad
// vendor fails the image rather than shipping.
//
// A section header names a file in static/; the fields under it are the record.
var (
	vendorSectionRe = regexp.MustCompile(`(?m)^## ([A-Za-z0-9._-]+\.[a-z]+)$`)
	vendorFieldRe   = regexp.MustCompile(`(?m)^- ([A-Za-z0-9-]+): (.+)$`)
)

type vendored struct {
	name   string
	fields map[string]string
}

// parseVendorRecords reads the "## <file>" sections of VENDOR.md. Prose
// sections whose header is not a filename ("## Updating") carry no fields we
// look for and are skipped by the header pattern.
func parseVendorRecords(t *testing.T) []vendored {
	t.Helper()

	raw, err := os.ReadFile("VENDOR.md")
	if err != nil {
		t.Fatalf("read VENDOR.md: %v", err)
	}
	doc := string(raw)

	heads := vendorSectionRe.FindAllStringSubmatchIndex(doc, -1)
	if len(heads) == 0 {
		t.Fatal("VENDOR.md records no files; expected at least one '## <file>' section")
	}

	var records []vendored
	for i, h := range heads {
		end := len(doc)
		if i+1 < len(heads) {
			end = heads[i+1][0]
		}
		rec := vendored{
			name:   doc[h[2]:h[3]],
			fields: map[string]string{},
		}
		for _, f := range vendorFieldRe.FindAllStringSubmatch(doc[h[1]:end], -1) {
			rec.fields[f[1]] = strings.TrimSpace(f[2])
		}
		records = append(records, rec)
	}
	return records
}

// TestVendoredAssetsMatchRecord is the integrity check: every file VENDOR.md
// describes must exist in the embedded FS and hash to the recorded digest.
// Because the digest covers the whole file, this subsumes any weaker check —
// the wrong release, a half-finished download, and a local patch all change it.
func TestVendoredAssetsMatchRecord(t *testing.T) {
	for _, rec := range parseVendorRecords(t) {
		t.Run(rec.name, func(t *testing.T) {
			for _, field := range []string{"Version", "Source", "SHA-256"} {
				if rec.fields[field] == "" {
					t.Errorf("VENDOR.md section %q has no %s field", rec.name, field)
				}
			}

			content, err := Static.ReadFile(path.Join("static", rec.name))
			if err != nil {
				t.Fatalf("VENDOR.md records %q but it is not embedded: %v", rec.name, err)
			}
			sum := sha256.Sum256(content)
			if got, want := hex.EncodeToString(sum[:]), rec.fields["SHA-256"]; got != want {
				t.Errorf("static/%s hashes to %s, VENDOR.md records %s\n"+
					"the file and its record disagree — re-download the recorded version, or update VENDOR.md if the change is deliberate",
					rec.name, got, want)
			}

			// The source URL pins the release it was fetched from, so a
			// Version bumped without re-fetching (or vice versa) shows up here
			// rather than as a silently stale download.
			if v := rec.fields["Version"]; v != "" && !strings.Contains(rec.fields["Source"], v) {
				t.Errorf("VENDOR.md records %s version %s but the source URL does not mention it: %s",
					rec.name, v, rec.fields["Source"])
			}
		})
	}
}

// htmxVersionRe matches htmx's own version declaration in dist/htmx.js
// ("version: '2.0.10'"). It is what htmx.version reports at runtime, so it is
// the authority on what the browser actually runs — worth checking separately
// from the digest, which proves only that the bytes are the bytes we recorded.
var htmxVersionRe = regexp.MustCompile(`version: '([^']+)'`)

func TestVendoredHTMXDeclaresRecordedVersion(t *testing.T) {
	var want string
	for _, rec := range parseVendorRecords(t) {
		if rec.name == "htmx.js" {
			want = rec.fields["Version"]
		}
	}
	if want == "" {
		t.Fatal("VENDOR.md has no '## htmx.js' section with a Version field")
	}

	content, err := Static.ReadFile("static/htmx.js")
	if err != nil {
		t.Fatalf("read embedded htmx: %v", err)
	}
	m := htmxVersionRe.FindSubmatch(content)
	if m == nil {
		t.Fatal("embedded htmx.js declares no version; a minified build or a truncated download?")
	}
	if got := string(m[1]); got != want {
		t.Errorf("embedded htmx.js declares version %s, VENDOR.md records %s", got, want)
	}
}
