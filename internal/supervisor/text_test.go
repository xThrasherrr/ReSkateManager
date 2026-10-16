package supervisor

import "testing"

func TestCleanText(t *testing.T) {
	for in, want := range map[string]string{
		"  hello  ":              "hello",
		"two\nlines\r\nhere":     "two lines  here",
		"tab\there":              "tab here",
		"ctrl-z\x1a gone":        "ctrl-z gone",
		"nul\x00 and del\x7f":    "nul and del",
		"c1\u0085 too":           "c1 too",
		"bad \xff bytes":         "bad  bytes",
		"héllo 🛹 — ünïcode":      "héllo 🛹 — ünïcode",
		"family 👨\u200d👩\u200d👧": "family 👨\u200d👩\u200d👧", // joiners stay
	} {
		if got := CleanText(in); got != want {
			t.Errorf("CleanText(%q) = %q, want %q", in, got, want)
		}
	}
	if got := CutText("aé", 2); got != "a" {
		t.Errorf("cut through a character: %q", got)
	}
}
