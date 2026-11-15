package appversion

import (
	"errors"
	"testing"
)

func TestParseAndCanonical(t *testing.T) {
	good := map[string]string{
		"1": "1.0.0", "1.2": "1.2.0", "1.2.3": "1.2.3", " 1.2.3 ": "1.2.3", "1.2.0.0": "1.2.0",
		"1.2.3.4": "1.2.3.4", "1.2.3.0": "1.2.3", "10.0.0": "10.0.0", "0": "0.0.0", "000001.2": "1.2.0",
	}
	for in, want := range good {
		got, err := Canonical(in)
		if err != nil || got != want {
			t.Errorf("Canonical(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "v1.2", "1.2.3-beta", "1..2", "1.2.", ".1", "a.b", "1.2.3.4.5", "-1", "1 2", "1234567", "1.2.3\n4"} {
		if _, err := ParseVersion(bad); !errors.Is(err, ErrBadVersion) {
			t.Errorf("ParseVersion(%q): %v", bad, err)
		}
	}
}

func TestCompareIsNumericNotText(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.10.0", "1.9.0", 1}, {"1.9.0", "1.10.0", -1}, {"1.2", "1.2.0", 0}, {"2", "1.99.99", 1},
		{"1.2.3.1", "1.2.3", 1}, {"0.0.1", "0.0.0", 1}, {"3.0.0", "3.0.0", 0},
	}
	for _, c := range cases {
		a, _ := ParseVersion(c.a)
		b, _ := ParseVersion(c.b)
		if got := a.Compare(b); got != c.want {
			t.Errorf("%s vs %s = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
