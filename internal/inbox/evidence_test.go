package inbox_test

import (
	"slices"
	"testing"

	"github.com/zigai/sesh/internal/inbox"
)

func TestParseEvidenceTakesTheItemsOfTheEvidenceSectionOnly(t *testing.T) {
	report := "**Changed**\n- see /work/shop/before.png\n\n**Evidence**\n" +
		"- Screenshot of the login page: `.cache/evidence/login.png`\n" +
		"- [the pull request](https://example.com/pr/4)\n" +
		"- recording at /work/shop/.cache/evidence/flow.mp4, and the log run.log.\n" +
		"- the same screenshot again: `.cache/evidence/login.png`\n" +
		"- checked by hand, nothing to show\n\n" +
		"**Not verified**\n- /work/shop/after.png\n"

	got := inbox.ParseEvidence(report, "/work/shop")
	want := []inbox.Evidence{
		{Target: "/work/shop/.cache/evidence/login.png", Kind: inbox.EvidenceImage},
		{Target: "https://example.com/pr/4", Kind: inbox.EvidenceURL},
		{Target: "/work/shop/.cache/evidence/flow.mp4", Kind: inbox.EvidenceVideo},
		{Target: "/work/shop/run.log", Kind: inbox.EvidenceText},
	}

	if !slices.Equal(got, want) {
		t.Fatalf("ParseEvidence() =\n%v\nwant\n%v", got, want)
	}
}

func TestParseEvidenceAcceptsTheHeadingStylesAgentsWrite(t *testing.T) {
	for _, heading := range []string{"**Evidence**", "**Evidence:**", "## Evidence", "### evidence"} {
		got := inbox.ParseEvidence("Done.\n\n"+heading+"\n- ~/shots/one.png\n", "/work")
		if want := []inbox.Evidence{{Target: "~/shots/one.png", Kind: inbox.EvidenceImage}}; !slices.Equal(got, want) {
			t.Errorf("heading %q: ParseEvidence() = %v, want %v", heading, got, want)
		}
	}
}

func TestParseEvidenceFindsNothingWithoutTheSection(t *testing.T) {
	if got := inbox.ParseEvidence("All done.\n- /work/shop/shot.png\n", "/work"); len(got) != 0 {
		t.Fatalf("ParseEvidence() = %v, want nothing", got)
	}
}
