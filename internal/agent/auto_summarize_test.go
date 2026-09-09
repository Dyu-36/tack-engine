package agent

import "testing"

func TestAutoSummarizeTokenLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		contextWindow int64
		want          int64
	}{
		{name: "unknown context remains disabled", contextWindow: 0, want: 0},
		{name: "64K keeps twenty percent reserve", contextWindow: 64_000, want: 51_200},
		{name: "128K keeps twenty percent reserve", contextWindow: 128_000, want: 102_400},
		{name: "150K keeps twenty percent reserve", contextWindow: 150_000, want: 120_000},
		{name: "200K compacts proactively", contextWindow: 200_000, want: 128_000},
		{name: "one million compacts proactively", contextWindow: 1_000_000, want: 128_000},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := autoSummarizeTokenLimit(test.contextWindow); got != test.want {
				t.Fatalf(
					"autoSummarizeTokenLimit(%d) = %d, want %d",
					test.contextWindow,
					got,
					test.want,
				)
			}
		})
	}
}
