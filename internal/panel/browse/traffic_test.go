package browse

import "testing"

func TestTrafficIsDropped(t *testing.T) {
	if TrafficIsDropped(0.1, 0.2) {
		t.Fatal("peak below min should not drop")
	}
	if !TrafficIsDropped(0.1, 2.0) {
		t.Fatal("10% of peak should drop")
	}
	if TrafficIsDropped(1.5, 2.0) {
		t.Fatal("75% of peak is healthy")
	}
	if TrafficDropPercent(0.2, 2.0) < 80 {
		t.Fatal("drop percent")
	}
}
