package app

import "testing"

// The reply room follows the model's output ceiling, within bounds, unless a
// compaction ratio is set.
func TestCompactReserveTracksModelOutputCeiling(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name                    string
		window, maxOutput, want int
		ratio                   float64
	}{
		{"small ceiling reserves only what it needs", 200_000, 8_192, 8_192, 0},
		{"large ceiling hits the cap", 1_000_000, 128_000, 20_000, 0},
		{"capped window clamps the reserve", 20_000, 64_000, 10_000, 0},
		{"unknown ceiling takes a share of the window", 128_000, 0, 16_384, 0},
		{"explicit ratio wins", 100_000, 64_000, 20_000, 0.8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := compactReserve(tc.window, tc.maxOutput, tc.ratio); got != tc.want {
				t.Fatalf("compactReserve = %d, want %d", got, tc.want)
			}
		})
	}
}
