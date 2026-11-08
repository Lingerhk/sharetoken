package version

import "testing"

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "1.0.1", -1},
		{"1.2.0", "1.1.9", 1},
		{"2.0.0", "1.99.99", 1},
		{"1.10.0", "1.9.0", 1},
		{"1.0.0-dev.abc", "1.0.0", -1},
		{"1.0.1-dev.abc", "1.0.0", 1},
		{"1.0.0-dev.abc", "1.0.0-dev.def", 0},
	} {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestValidAndDev(t *testing.T) {
	for v, want := range map[string][2]bool{ // valid, dev
		"1.0.0":         {true, false},
		"1.0.0-dev.abc": {true, true},
		"dev":           {false, true},
		"":              {false, true},
		"1.0":           {false, true},
		"v1.0.0":        {false, true},
		"1.0.x":         {false, true},
		"1.-1.0":        {false, true},
	} {
		if Valid(v) != want[0] || IsDev(v) != want[1] {
			t.Errorf("%q: valid %v dev %v, want %v", v, Valid(v), IsDev(v), want)
		}
	}
}
