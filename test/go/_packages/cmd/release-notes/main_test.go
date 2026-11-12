package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateNotesFromPreviousMainlineRelease(t *testing.T) {
	dir := newGitRepository(t)
	commit(t, dir, "feat: initial release")
	base := "0.1.1-" + mustGit(t, dir, "rev-parse", "--short=7", "HEAD")
	mustGit(t, dir, "tag", "-a", base, "-m", "first release")
	mustGit(t, dir, "switch", "-c", "feature")
	side := commit(t, dir, "feat(parser): support Unicode")
	mustGit(t, dir, "tag", "0.1.2-"+side[:7])

	mustGit(t, dir, "switch", "main")
	fix := commit(t, dir, "fix(ui): handle [empty] <value>")
	mustGit(t, dir, "tag", "0.1.3-preview")
	mustGit(t, dir, "merge", "--no-ff", "-m", "Merge feature branch", "feature")
	head := commit(t, dir, "ci: publish the changelog")
	mustGit(t, dir, "tag", "0.1.5-"+head[:7])

	repo := "https://github.com/example/yagp"
	notes, err := generateNotes(dir, repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"### Features\n\n- feat(parser): support Unicode ([" + side[:7] + "](" + repo + "/commit/" + side + "))",
		"### Fixes\n\n- fix(ui): handle \\[empty\\] &lt;value&gt; ([" + fix[:7] + "](" + repo + "/commit/" + fix + "))",
		"### Build and CI\n\n- ci: publish the changelog",
		"### Other changes\n\n- Merge feature branch",
		"[Full changelog](" + repo + "/compare/" + base + "..." + head + ")",
	} {
		if !strings.Contains(notes, want) {
			t.Errorf("release notes are missing %q:\n%s", want, notes)
		}
	}
	if strings.Contains(notes, "feat: initial release") {
		t.Fatalf("previous release was repeated:\n%s", notes)
	}
}

func TestGenerateNotesForFirstRelease(t *testing.T) {
	dir := newGitRepository(t)
	initial := commit(t, dir, "初始提交")
	commit(t, dir, "feat(api)!: replace old endpoint")
	commit(t, dir, "docs: explain the new API")
	commit(t, dir, "test: verify the new API")
	commit(t, dir, "refactor: simplify code")
	notes, err := generateNotes(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"### Breaking changes\n\n- feat(api)!: replace old endpoint",
		"### Documentation\n\n- docs: explain the new API",
		"### Tests\n\n- test: verify the new API",
		"### Maintenance\n\n- refactor: simplify code",
		"- 初始提交 (`" + initial[:7] + "`)",
	} {
		if !strings.Contains(notes, want) {
			t.Errorf("release notes are missing %q:\n%s", want, notes)
		}
	}
	if strings.Contains(notes, "/compare/") {
		t.Fatalf("a first release should not have a comparison base:\n%s", notes)
	}
}

func TestGenerateNotesRequiresFullHistory(t *testing.T) {
	dir := newGitRepository(t)
	commit(t, dir, "feat: initial commit")
	commit(t, dir, "fix: latest commit")
	dest := filepath.Join(t.TempDir(), "shallow")
	mustGit(t, dir, "clone", "--no-local", "--depth=1", dir, dest)
	if _, err := generateNotes(dest, ""); err == nil || !strings.Contains(err.Error(), "requires full Git history") {
		t.Fatalf("shallow checkout error = %v", err)
	}
}

func TestGenerateNotesHandlesEmptyHistoryAndSubjects(t *testing.T) {
	dir := newGitRepository(t)
	if _, err := generateNotes(dir, ""); err == nil {
		t.Fatal("an empty repository produced release notes")
	}
	mustGit(t, dir, "commit", "--allow-empty", "--allow-empty-message", "-m", "")
	commit(t, dir, "perf: reduce startup latency")
	commit(t, dir, "deploy: ship to staging")
	notes, err := generateNotes(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"### Fixes\n\n- perf: reduce startup latency", "### Other changes\n\n- deploy: ship to staging", "- (no subject)"} {
		if !strings.Contains(notes, want) {
			t.Errorf("release notes are missing %q:\n%s", want, notes)
		}
	}
}

func newGitRepository(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, "init", "--initial-branch=main", "--object-format=sha1")
	mustGit(t, dir, "config", "user.name", "Release Test")
	mustGit(t, dir, "config", "user.email", "release@example.test")
	mustGit(t, dir, "config", "commit.gpgSign", "false")
	mustGit(t, dir, "config", "tag.gpgSign", "false")
	return dir
}

func commit(t *testing.T, dir, subject string) string {
	t.Helper()
	mustGit(t, dir, "commit", "--allow-empty", "-m", subject)
	return mustGit(t, dir, "rev-parse", "HEAD")
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := git(dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}
