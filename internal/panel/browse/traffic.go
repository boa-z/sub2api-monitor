package browse

// TrafficDropRatio is the current/peak QPS ratio below which we treat traffic as dropped.
const TrafficDropRatio = 0.2

// MinPeakQPSForDrop is the minimum peak needed before drop detection fires.
// Avoids noisy alerts when peak itself is tiny.
const MinPeakQPSForDrop = 0.5

// TrafficIsDropped reports whether current QPS looks abnormally low vs peak.
func TrafficIsDropped(current, peak float64) bool {
	if peak < MinPeakQPSForDrop {
		return false
	}
	if current < 0 {
		current = 0
	}
	return current <= peak*TrafficDropRatio
}

// TrafficDropPercent returns how far below peak current is (0..100).
// If not dropped / peak too small, returns 0.
func TrafficDropPercent(current, peak float64) int {
	if !TrafficIsDropped(current, peak) {
		return 0
	}
	if peak <= 0 {
		return 0
	}
	pct := (1 - current/peak) * 100
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return int(pct + 0.5)
}
