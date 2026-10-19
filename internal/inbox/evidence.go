package inbox

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const (
	EvidenceURL   EvidenceKind = "url"
	EvidenceImage EvidenceKind = "image"
	EvidenceVideo EvidenceKind = "video"
	EvidenceText  EvidenceKind = "text"
	EvidenceFile  EvidenceKind = "file"
)

var (
	evidenceHeading = regexp.MustCompile(`(?i)^\s*(?:#{1,6}\s*evidence\s*:?\s*|\*\*\s*evidence\s*:?\s*\*\*\s*:?)\s*$`)
	sectionHeading  = regexp.MustCompile(`^\s*(?:#{1,6}\s+\S|\*\*[^*]+\*\*\s*:?\s*$)`)
	listItem        = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+(.*)$`)
	markdownLink    = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)\)`)
	codeSpan        = regexp.MustCompile("`([^`]+)`")

	imageExtensions = []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".bmp"}
	videoExtensions = []string{".mp4", ".webm", ".mkv", ".mov"}
	textExtensions  = []string{".txt", ".log", ".md", ".json", ".jsonl", ".csv", ".diff", ".patch", ".cast", ".out"}
)

type EvidenceKind string

// Evidence is one item of a report's Evidence section: a URL, or a file path that is absolute,
// under "~/", or made absolute against the agent's directory.
type Evidence struct {
	Target string
	Kind   EvidenceKind
}

// ParseEvidence lists the items of the Evidence section of an agent's report, in order and once
// each. The section starts at an "**Evidence**" or "## Evidence" line and ends at the next
// heading. Each list item gives its link targets, else its code spans, else the words that look
// like a URL or a path.
func ParseEvidence(report, dir string) []Evidence {
	var items []Evidence

	inside := false

	for line := range strings.Lines(report) {
		line = strings.TrimRight(line, "\r\n")

		switch {
		case evidenceHeading.MatchString(line):
			inside = true

			continue
		case inside && sectionHeading.MatchString(line):
			inside = false
		}

		if !inside {
			continue
		}

		match := listItem.FindStringSubmatch(line)
		if match == nil {
			continue
		}

		for _, target := range itemTargets(match[1]) {
			item, ok := newEvidence(target, dir)
			if ok && !slices.Contains(items, item) {
				items = append(items, item)
			}
		}
	}

	return items
}

func itemTargets(item string) []string {
	if links := markdownLink.FindAllStringSubmatch(item, -1); links != nil {
		return submatches(links)
	}

	if spans := codeSpan.FindAllStringSubmatch(item, -1); spans != nil {
		return slices.DeleteFunc(submatches(spans), func(span string) bool { return !looksLikeTarget(span) })
	}

	words := strings.Fields(item)
	for index, word := range words {
		words[index] = strings.TrimRight(word, ".,;:)")
	}

	return slices.DeleteFunc(words, func(word string) bool { return !looksLikeTarget(word) })
}

func submatches(matches [][]string) []string {
	values := make([]string, 0, len(matches))
	for _, match := range matches {
		values = append(values, strings.TrimSpace(match[1]))
	}

	return values
}

func looksLikeTarget(text string) bool {
	switch {
	case strings.ContainsAny(text, " \t"):
		return false
	case strings.HasPrefix(text, "http://"), strings.HasPrefix(text, "https://"):
		return true
	case strings.HasPrefix(text, "/"), strings.HasPrefix(text, "~/"), strings.HasPrefix(text, "./"), strings.HasPrefix(text, "../"):
		return true
	default:
		return strings.Contains(text, "/") || kindOf(text) != EvidenceFile
	}
}

func newEvidence(target, dir string) (Evidence, bool) {
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		return Evidence{Target: target, Kind: EvidenceURL}, true
	}

	if !filepath.IsAbs(target) && !strings.HasPrefix(target, "~/") {
		if !filepath.IsAbs(dir) {
			return Evidence{Target: "", Kind: ""}, false
		}

		target = filepath.Join(dir, target)
	}

	return Evidence{Target: target, Kind: kindOf(target)}, true
}

func kindOf(path string) EvidenceKind {
	extension := strings.ToLower(filepath.Ext(path))

	switch {
	case slices.Contains(imageExtensions, extension):
		return EvidenceImage
	case slices.Contains(videoExtensions, extension):
		return EvidenceVideo
	case slices.Contains(textExtensions, extension):
		return EvidenceText
	default:
		return EvidenceFile
	}
}
