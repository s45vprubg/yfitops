package game

import "testing"

// These tests pin the scoring contract (design_doc §7). The stage frontend
// mirrors this formula in JS; if these change, the visual timer drifts.

func TestMaxPointsForRow(t *testing.T) {
	want := map[int]int{1: 100, 2: 125, 3: 150, 4: 175, 5: 200}
	for row, exp := range want {
		if got := MaxPointsForRow(row); got != exp {
			t.Errorf("MaxPointsForRow(%d) = %d, want %d", row, got, exp)
		}
	}
}

func TestCurrentPoints_HoldThenDecay(t *testing.T) {
	// Row 5: max 200, base 100, bonus 100.
	// t<=5s holds at 200.
	if got := CurrentPoints(5, 0); got != 200 {
		t.Errorf("t=0 row5 = %d, want 200", got)
	}
	if got := CurrentPoints(5, 5000); got != 200 {
		t.Errorf("t=5s row5 = %d, want 200", got)
	}
	// t>=60s floors to base 100.
	if got := CurrentPoints(5, 60000); got != 100 {
		t.Errorf("t=60s row5 = %d, want 100", got)
	}
	if got := CurrentPoints(5, 120000); got != 100 {
		t.Errorf("t=120s row5 = %d, want 100", got)
	}
	// Midpoint: t=32.5s is halfway through the 5..60 window -> bonus halved.
	// frac = 1 - (32.5-5)/55 = 0.5 -> 100 + 100*0.5 = 150.
	if got := CurrentPoints(5, 32500); got != 150 {
		t.Errorf("t=32.5s row5 = %d, want 150", got)
	}
}

func TestCurrentPoints_Row1NoBonus(t *testing.T) {
	// Row 1 has no bonus to decay; always 100.
	for _, ms := range []int64{0, 5000, 30000, 60000, 90000} {
		if got := CurrentPoints(1, ms); got != 100 {
			t.Errorf("row1 t=%dms = %d, want 100", ms, got)
		}
	}
}

func TestCurrentPoints_MonotonicNonIncreasing(t *testing.T) {
	prev := CurrentPoints(4, 0)
	for ms := int64(0); ms <= 70000; ms += 250 {
		got := CurrentPoints(4, ms)
		if got > prev {
			t.Fatalf("points increased at t=%dms: %d > %d", ms, got, prev)
		}
		prev = got
	}
}

func TestPartialAward(t *testing.T) {
	// Within hold window on row 5: current=200, partial takes 50, leaves 150.
	awarded, remaining := PartialAward(5, 0)
	if awarded != 50 {
		t.Errorf("awarded = %d, want 50", awarded)
	}
	if remaining != 150 {
		t.Errorf("remaining = %d, want 150", remaining)
	}
	// After full decay on row 1: current=100, partial takes 50, leaves 50.
	awarded, remaining = PartialAward(1, 90000)
	if awarded != 50 || remaining != 50 {
		t.Errorf("row1 late partial = (%d,%d), want (50,50)", awarded, remaining)
	}
}

// ---------------------------------------------------------------------------
// currentPointsFromPool — the post-partial decay curve. Defined in engine.go
// (scoring.go is a locked contract file and cannot host it), mirrored in JS by
// web/shared/scoring.ts, which the stage calls every animation frame to paint
// the live point value. Three copies of one formula: these are the vectors all
// three must reproduce. If you change the curve, change it in all three and
// update this table.
// ---------------------------------------------------------------------------

// TestCurrentPointsFromPool_MatchesCurrentPoints proves the pool form is a
// generalization, not a divergence: fed the row-derived ceiling and the standard
// base it must agree with CurrentPoints exactly, at every row, across the whole
// hold/decay/floor range. This is the assertion that catches one copy of the
// formula being edited without the other.
func TestCurrentPointsFromPool_MatchesCurrentPoints(t *testing.T) {
	for row := 1; row <= 5; row++ {
		for ms := int64(0); ms <= 70000; ms += 125 {
			want := CurrentPoints(row, ms)
			got := currentPointsFromPool(MaxPointsForRow(row), BaseValue, ms)
			if got != want {
				t.Fatalf("row %d t=%dms: pool form = %d, CurrentPoints = %d", row, ms, got, want)
			}
		}
	}
}

// TestCurrentPointsFromPool_ShiftedPool covers what CurrentPoints cannot express:
// the reduced ceiling/floor left alive after a partial guess. The pool below is
// the real one from a row-5 partial at t=0 — PartialAward leaves remaining=150,
// and gradeCorrect pairs it with baseP = BaseValue-PartialPoints = 50.
func TestCurrentPointsFromPool_ShiftedPool(t *testing.T) {
	const maxP, baseP = 150, 50 // bonus = 100

	cases := []struct {
		name string
		ms   int64
		want int
	}{
		{"t=0 holds at ceiling", 0, 150},
		{"t=5s still holds (boundary inclusive)", 5000, 150},
		// frac = 1 - (10-5)/55 = 0.9090..., 50 + 90.909... = 140.909... -> 140.
		// The only vector here that distinguishes floor from round; keep it.
		{"t=10s floors a fractional value", 10000, 140},
		// frac = 1 - (32.5-5)/55 = 0.5 -> 50 + 50 = 100.
		{"t=32.5s halfway", 32500, 100},
		{"t=60s drops to floor (boundary inclusive)", 60000, 50},
		{"t=120s stays at floor", 120000, 50},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := currentPointsFromPool(maxP, baseP, c.ms); got != c.want {
				t.Errorf("currentPointsFromPool(%d, %d, %d) = %d, want %d",
					maxP, baseP, c.ms, got, c.want)
			}
		})
	}
}

// TestCurrentPointsFromPool_DegenerateAndMonotonic covers the collapsed pool (a
// row-1 partial leaves max == base, so there is no bonus to decay) and asserts
// the curve never rewards waiting.
func TestCurrentPointsFromPool_DegenerateAndMonotonic(t *testing.T) {
	// Row 1 partial: PartialAward(1, 0) leaves remaining=50, and gradeCorrect
	// clamps baseP down to maxP. No bonus -> flat.
	for _, ms := range []int64{0, 5000, 30000, 60000, 90000} {
		if got := currentPointsFromPool(50, 50, ms); got != 50 {
			t.Errorf("flat pool t=%dms = %d, want 50", ms, got)
		}
	}

	prev := currentPointsFromPool(150, 50, 0)
	for ms := int64(0); ms <= 70000; ms += 250 {
		got := currentPointsFromPool(150, 50, ms)
		if got > prev {
			t.Fatalf("pool points increased at t=%dms: %d > %d", ms, got, prev)
		}
		if got < 50 {
			t.Fatalf("pool points fell below the floor at t=%dms: %d", ms, got)
		}
		prev = got
	}
}

func TestDailyDoubleMultiplier(t *testing.T) {
	cases := []struct {
		stars float64
		want  float64
	}{
		{5.0, 2.00}, {4.0, 1.75}, {3.0, 1.50}, {2.0, 1.25}, {1.0, 1.00},
	}
	for _, c := range cases {
		if got := DailyDoubleMultiplier(c.stars); got != c.want {
			t.Errorf("DailyDoubleMultiplier(%.1f) = %.2f, want %.2f", c.stars, got, c.want)
		}
	}
}
