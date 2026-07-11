package browse

import "testing"

func TestParseDurationLabel(t *testing.T) {
	if ParseDurationLabel("15m") != 15*60 {
		t.Fatal("15m")
	}
	if ParseDurationLabel("1h") != 3600 {
		t.Fatal("1h")
	}
	if ParseDurationLabel("nope") != 0 {
		t.Fatal("nope")
	}
}

func TestParseFlexibleDuration(t *testing.T) {
	cases := []struct {
		in      string
		wantSec int64
		wantLab string
		wantErr bool
	}{
		{"15m", 15 * 60, "15m", false},
		{"45m", 45 * 60, "45m", false},
		{"2h", 2 * 3600, "2h", false},
		{"90", 90 * 60, "90m", false},
		{"1.5h", 5400, "1.5h", false},
		{"", 0, "", true},
		{"bad", 0, "", true},
		{"0", 0, "", true},
	}
	for _, tc := range cases {
		sec, lab, err := ParseFlexibleDuration(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%q: expected err", tc.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if sec != tc.wantSec || lab != tc.wantLab {
			t.Fatalf("%q: sec=%d lab=%s want %d %s", tc.in, sec, lab, tc.wantSec, tc.wantLab)
		}
	}
}
