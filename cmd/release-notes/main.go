package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

const (
	breaking = iota
	features
	fixes
	documentation
	buildAndCI
	tests
	maintenance
	other
)

var (
	sectionTitles = [...]string{
		breaking:      "Breaking changes",
		features:      "Features",
		fixes:         "Fixes",
		documentation: "Documentation",
		buildAndCI:    "Build and CI",
		tests:         "Tests",
		maintenance:   "Maintenance",
		other:         "Other changes",
	}
	releaseTag     = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+-[0-9a-f]{7}$`)
	commitType     = regexp.MustCompile(`^([a-z]+)(?:\([^)]*\))?(!)?:`)
	escapeMarkdown = strings.NewReplacer(
		"&", "&amp;", "<", "&lt;", ">", "&gt;",
		"\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "~", "\\~",
	)
)

func main() {
	repository := flag.String("repository", "", "repository URL for commit and comparison links")
	flag.Parse()
	notes, err := generateNotes(".", strings.TrimRight(*repository, "/"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Print(notes)
}

func generateNotes(dir, repository string) (string, error) {
	shallow, err := git(dir, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(shallow) == "true" {
		return "", fmt.Errorf("generating release notes requires full Git history and tags")
	}
	history, err := git(dir, "rev-list", "--first-parent", "HEAD")
	if err != nil {
		return "", err
	}
	lineage := strings.Fields(history)
	head := lineage[0]
	tags, err := git(dir, "for-each-ref", "--sort=-version:refname", "--format=%(objectname)%09%(*objectname)%09%(refname:strip=2)", "refs/tags/")
	if err != nil {
		return "", err
	}

	// Match the version counter's first-parent history. Skip HEAD so reruns
	// produce the same range after their release tag has been created.
	base := previousTag(lineage[1:], tags)
	revisions := head
	if base != "" {
		revisions = "refs/tags/" + base + ".." + head
	}
	log, err := git(dir, "log", "--topo-order", "--format=%H%x09%s", revisions, "--")
	if err != nil {
		return "", err
	}
	return renderNotes(log, base, head, repository), nil
}

func previousTag(ancestors []string, refs string) string {
	tagsByCommit := make(map[string]string)
	for line := range strings.SplitSeq(strings.TrimSpace(refs), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 3 || !releaseTag.MatchString(fields[2]) {
			continue
		}
		commit, peeled, tag := fields[0], fields[1], fields[2]
		if peeled != "" {
			commit = peeled
		}
		if _, exists := tagsByCommit[commit]; !exists {
			tagsByCommit[commit] = tag
		}
	}
	for _, commit := range ancestors {
		if tag := tagsByCommit[commit]; tag != "" {
			return tag
		}
	}
	return ""
}

func renderNotes(log, base, head, repository string) string {
	var groups [len(sectionTitles)][]string
	for line := range strings.SplitSeq(strings.TrimRight(log, "\n"), "\n") {
		sha, subject, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		ref := "`" + sha[:7] + "`"
		if repository != "" {
			ref = "[" + sha[:7] + "](" + repository + "/commit/" + sha + ")"
		}
		if subject == "" {
			subject = "(no subject)"
		}
		index := sectionFor(subject)
		groups[index] = append(groups[index], "- "+escapeMarkdown.Replace(subject)+" ("+ref+")\n")
	}

	var notes strings.Builder
	notes.WriteString("## Changes\n")
	for i, title := range sectionTitles {
		if len(groups[i]) > 0 {
			fmt.Fprintf(&notes, "\n### %s\n\n%s", title, strings.Join(groups[i], ""))
		}
	}
	if repository != "" && base != "" {
		fmt.Fprintf(&notes, "\n[Full changelog](%s/compare/%s...%s)\n", repository, base, head)
	}
	return notes.String()
}

func sectionFor(subject string) int {
	matches := commitType.FindStringSubmatch(subject)
	if matches == nil {
		return other
	}
	if matches[2] == "!" {
		return breaking
	}
	switch matches[1] {
	case "feat":
		return features
	case "fix", "perf", "revert":
		return fixes
	case "docs":
		return documentation
	case "build", "ci":
		return buildAndCI
	case "test":
		return tests
	case "chore", "refactor", "style":
		return maintenance
	default:
		return other
	}
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if exit, ok := err.(*exec.ExitError); ok {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(exit.Stderr)))
	}
	return string(out), err
}
