package app

import "testing"

// A compaction ratio, when set, overrides the output ceiling.
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

// A sub-agent's history compacts by the window of the model it runs, not
// the conversation's: a 200k model under a 1M one would only compact once
// its requests failed.
func TestSubagentsCompactByTheirModel(t *testing.T) {
	e := boot(t, setup{}, nil) // claude-sonnet-4-5, 1M
	sub, err := e.app.subagentModel("anthropic", "claude-opus-4-5")
	if err != nil {
		t.Fatal(err)
	}
	if main := e.app.Current().model.compactAt; main != 980_000 || sub.CompactAt != 180_000 {
		t.Fatalf("compact at %d for the conversation, %d for the sub-agent", main, sub.CompactAt)
	}
}
