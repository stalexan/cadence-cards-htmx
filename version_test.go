package cadence

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// semverRe allows the pre-release ladder documented in docs/VERSIONING.md
// (0.1.0-alpha, 0.1.0-beta, 0.9.0-rc1) but not build metadata, which nothing
// here produces.
var semverRe = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?$`)

// TestVersionWellFormed runs inside the Docker build (Dockerfile runs
// "go test ./..."), so a malformed VERSION fails the image build rather than
// shipping a blank version badge.
func TestVersionWellFormed(t *testing.T) {
	if Version == "" {
		t.Fatal("VERSION is empty")
	}
	if Version != strings.TrimSpace(Version) {
		t.Errorf("Version %q has surrounding whitespace", Version)
	}
	if strings.ContainsAny(Version, " \t\n") {
		t.Errorf("Version %q contains whitespace", Version)
	}
	if !semverRe.MatchString(Version) {
		t.Errorf("Version %q is not MAJOR.MINOR.PATCH[-prerelease]", Version)
	}
}

// TestVersionMatchesGitTag catches a tag that disagrees with VERSION. It can
// only check tagged commits, and it must skip where git is unavailable — most
// importantly inside the Docker build, where .dockerignore strips .git.
func TestVersionMatchesGitTag(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if err := exec.Command("git", "rev-parse", "--git-dir").Run(); err != nil {
		t.Skip("not a git checkout (e.g. inside the Docker build, where .git is .dockerignored)")
	}
	out, err := exec.Command("git", "tag", "--points-at", "HEAD").Output()
	if err != nil {
		t.Skip("cannot list tags:", err)
	}

	var tags []string
	for _, line := range strings.Split(string(out), "\n") {
		if tag := strings.TrimSpace(line); tag != "" {
			tags = append(tags, tag)
		}
	}
	if len(tags) == 0 {
		t.Skip("HEAD is not tagged")
	}

	want := "v" + Version
	for _, tag := range tags {
		if tag == want {
			return
		}
	}
	t.Errorf("HEAD is tagged %v but VERSION says %q; want tag %s — fix VERSION or move the tag (see docs/VERSIONING.md)",
		tags, Version, want)
}
