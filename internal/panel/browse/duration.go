package browse

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseDurationLabel maps preset tokens (15m/1h/6h/24h) to seconds.
func ParseDurationLabel(s string) int64 {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "15m":
		return 15 * 60
	case "1h":
		return 60 * 60
	case "6h":
		return 6 * 60 * 60
	case "24h":
		return 24 * 60 * 60
	default:
		return 0
	}
}

// ParseFlexibleDuration accepts 30m / 2h / 1d / bare minutes (1..10080).
// Caps at 7 days; minimum 1 minute.
func ParseFlexibleDuration(raw string) (sec int64, label string, err error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	s = strings.ReplaceAll(s, " ", "")
	if s == "" {
		return 0, "", fmt.Errorf("时长不能为空")
	}
	if p := ParseDurationLabel(s); p > 0 {
		return p, s, nil
	}
	if n, e := strconv.ParseInt(s, 10, 64); e == nil {
		if n < 1 || n > 7*24*60 {
			return 0, "", fmt.Errorf("分钟需在 1–10080（7 天）")
		}
		return n * 60, fmt.Sprintf("%dm", n), nil
	}
	var numStr, unit string
	for i, r := range s {
		if (r >= '0' && r <= '9') || r == '.' {
			continue
		}
		numStr = s[:i]
		unit = s[i:]
		break
	}
	if numStr == "" || unit == "" {
		return 0, "", fmt.Errorf("无法解析时长")
	}
	n, e := strconv.ParseFloat(numStr, 64)
	if e != nil || n <= 0 {
		return 0, "", fmt.Errorf("时长数字无效")
	}
	var mult float64
	switch unit {
	case "m", "min", "mins", "minute", "minutes":
		mult = 60
		label = fmt.Sprintf("%gm", n)
	case "h", "hr", "hrs", "hour", "hours":
		mult = 3600
		label = fmt.Sprintf("%gh", n)
	case "d", "day", "days":
		mult = 86400
		label = fmt.Sprintf("%gd", n)
	default:
		return 0, "", fmt.Errorf("未知单位 %s（用 m/h/d）", unit)
	}
	secF := n * mult
	if secF < 60 {
		return 0, "", fmt.Errorf("最短 1 分钟")
	}
	if secF > 7*24*3600 {
		return 0, "", fmt.Errorf("最长 7 天")
	}
	return int64(secF + 0.5), label, nil
}
