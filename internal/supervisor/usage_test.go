//go:build windows || linux

package supervisor

import (
	"os"
	"testing"
)

func TestUsageOfSelf(t *testing.T) {
	// Burn a little CPU so the reading is above zero.
	x := 0
	for i := range 50_000_000 {
		x += i
	}
	_ = x
	u, err := usage(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if u.CPU <= 0 || u.Mem < 1<<20 {
		t.Errorf("usage %+v", u)
	}
}
