package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ParseBytes reads a size such as "10485760", "512k", "256M", "256MiB",
// "256MB" or "1.5G". Units are binary and case-insensitive: k, M, G and T
// are powers of 1024, with or without a trailing "B" or "iB", the same way
// Docker reads a tmpfs size. A plain number is bytes.
func ParseBytes(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
		i++
	}
	num, unit := s[:i], strings.ToLower(strings.TrimSpace(s[i:]))
	if num == "" {
		return 0, fmt.Errorf("size %q: no number", s)
	}
	unit = strings.TrimSuffix(strings.TrimSuffix(unit, "ib"), "b")
	var mult float64
	switch unit {
	case "":
		mult = 1
	case "k":
		mult = 1 << 10
	case "m":
		mult = 1 << 20
	case "g":
		mult = 1 << 30
	case "t":
		mult = 1 << 40
	default:
		return 0, fmt.Errorf("size %q: unknown unit (use k, M, G or T)", s)
	}
	if unit == "" && !strings.Contains(num, ".") {
		n, err := strconv.ParseInt(num, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("size %q: %w", s, err)
		}
		return n, nil
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, fmt.Errorf("size %q: %w", s, err)
	}
	v := f * mult
	if v >= math.MaxInt64 {
		return 0, fmt.Errorf("size %q is too large", s)
	}
	return int64(v), nil
}

// FormatBytes renders n with a binary unit, for log lines.
func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	v := float64(n)
	i := -1
	for v >= unit && i < len(units)-1 {
		v /= unit
		i++
	}
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f %s", v, units[i])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// positiveBytesEnv parses a positive size from key into dst.
func positiveBytesEnv(getenv func(string) string, key string, dst *int64) error {
	v, ok := lookup(getenv, key)
	if !ok {
		return nil
	}
	n, err := ParseBytes(v)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	if n <= 0 {
		return fmt.Errorf("%s must be > 0", key)
	}
	*dst = n
	return nil
}
