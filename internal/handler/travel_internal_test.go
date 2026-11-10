package handler

import (
	"strings"
	"testing"
)

func TestDurationToMinutesRoundsUp(t *testing.T) {
	for in, want := range map[string]int{"0s": 0, "1s": 1, "60s": 1, "61s": 2, "1800s": 30, "1799.5s": 30} {
		got, err := durationToMinutes(in)
		if err != nil || got != want {
			t.Errorf("%q = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := durationToMinutes("soon"); err == nil {
		t.Error("a malformed duration must be an error")
	}
}

func TestSkinNeverShipsTheVersionPlaceholder(t *testing.T) {
	if strings.Contains(bookmeetSkinHTML, "__BM_VERSION__") {
		t.Fatal("the skin must have its version placeholder substituted at start-up")
	}
}
