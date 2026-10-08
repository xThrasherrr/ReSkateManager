package supervisor

import (
	"slices"
	"strings"
	"testing"
)

// A line over MaxLine is cut and the reading goes on: the lines after it still
// arrive. Line endings go, as with bufio.Scanner.
func TestReadLinesCutsLongLines(t *testing.T) {
	long := strings.Repeat("x", 2*MaxLine+123)
	in := "first\r\n\n" + long + "\nafter\r\nno newline at the end"
	out := make(chan string, 16)
	readLines(strings.NewReader(in), out)
	close(out)
	var got []string
	for l := range out {
		got = append(got, l)
	}
	want := []string{"first", "", strings.Repeat("x", MaxLine) + cutMark, "after", "no newline at the end"}
	if !slices.Equal(got, want) {
		for i := range max(len(got), len(want)) {
			var g, w string
			if i < len(got) {
				g = got[i]
			}
			if i < len(want) {
				w = want[i]
			}
			if g != w {
				t.Errorf("line %d: got %d bytes %q…, want %d bytes %q…", i, len(g), g[:min(len(g), 20)], len(w), w[:min(len(w), 20)])
			}
		}
		t.Fatalf("got %d lines, want %d", len(got), len(want))
	}
}

// A line exactly MaxLine long is kept whole.
func TestReadLinesKeepsALineAtTheLimit(t *testing.T) {
	out := make(chan string, 2)
	readLines(strings.NewReader(strings.Repeat("y", MaxLine)+"\n"), out)
	if l := <-out; len(l) != MaxLine || strings.HasSuffix(l, cutMark) {
		t.Errorf("got %d bytes, cut: %v", len(l), strings.HasSuffix(l, cutMark))
	}
}
