package history

import (
	"math"
	"os"
	"testing"
)

func TestLocalPIDStateClassifiesImpossibleAndCurrentProcess(t *testing.T) {
	for _, pid := range []int64{-1, 0, math.MaxInt64} {
		if got := LocalPIDState(pid); got != PIDDead {
			t.Errorf("LocalPIDState(%d) = %v, want PIDDead", pid, got)
		}
	}
	if got := LocalPIDState(int64(os.Getpid())); got != PIDAlive {
		t.Fatalf("LocalPIDState(current pid) = %v, want PIDAlive", got)
	}
}
