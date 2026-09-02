package vise

import "testing"

// A metric's direction decides which way is worse, and that is the whole
// difference between a quality gate and a random one. Inverting the "up" case
// left the suite green, because every metric fixture in it counted downwards.
//
// The table covers both directions and both enforcement settings, so no single
// branch of the regression rule can be removed without a failure here.
func TestMetricRegressionDependsOnDirectionAndEnforcement(t *testing.T) {
	tests := []struct {
		name      string
		direction string
		enforce   string
		base      float64
		now       float64
		regressed bool
	}{
		{"lower is better and it rose", "down", "no-regress", 10, 12, true},
		{"lower is better and it fell", "down", "no-regress", 10, 8, false},
		{"lower is better and it held", "down", "no-regress", 10, 10, false},
		{"higher is better and it fell", "up", "no-regress", 10, 8, true},
		{"higher is better and it rose", "up", "no-regress", 10, 12, false},
		{"higher is better and it held", "up", "no-regress", 10, 10, false},
		{"tracked only, moving the wrong way", "down", "none", 10, 12, false},
		{"tracked only, higher is better", "up", "none", 10, 8, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := metricRegressed(test.direction, test.enforce, test.base, test.now)
			if got != test.regressed {
				t.Fatalf("regressed = %t, want %t for %s %s %v -> %v",
					got, test.regressed, test.direction, test.enforce, test.base, test.now)
			}
		})
	}
}
