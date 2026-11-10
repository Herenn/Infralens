package mcp

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// timeArgHelp is the shared description of every time-valued argument.
const timeArgHelp = `RFC 3339 timestamp ("2026-11-04T15:00:00Z"), a date ("2026-11-04", midnight UTC), "now", or a relative offset into the past such as "24h", "7d", "2w", "90m" or "1d12h" (an optional " ago" suffix or leading "-" is accepted)`

// futureSlack tolerates client/server clock skew before a timestamp counts as
// "in the future".
const futureSlack = 2 * time.Minute

// parseTimeArg parses a flexible point-in-time argument relative to now.
// Relative values always mean the past: "24h" is 24 hours ago.
func parseTimeArg(arg, raw string, now time.Time) (time.Time, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}, invalidArgs("%s is empty; expected %s", arg, timeArgHelp)
	}
	if strings.EqualFold(s, "now") {
		return now, nil
	}

	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			if t.After(now.Add(futureSlack)) {
				return time.Time{}, invalidArgs("%s %q is in the future (server time is %s)", arg, s, now.UTC().Format(time.RFC3339))
			}
			return toServerZone(t), nil
		}
	}

	rel := strings.ToLower(s)
	rel = strings.TrimSpace(strings.TrimSuffix(rel, "ago"))
	rel = strings.TrimPrefix(rel, "-")
	d, err := parseFlexibleDuration(rel)
	if err == nil {
		if d <= 0 {
			return now, nil
		}
		return now.Add(-d), nil
	}
	if strings.Contains(err.Error(), "limit") {
		return time.Time{}, invalidArgs("%s %q is too far in the past: %v", arg, s, err)
	}
	return time.Time{}, invalidArgs("%s %q is not a recognised time; expected %s", arg, s, timeArgHelp)
}

// toServerZone is a TEMPORARY workaround for a storage bug, isolated here so
// it can be deleted in one place.
//
// The SQLite store persists timestamps as text in the zone they were written
// in (time.Now(), i.e. time.Local) and compares them as text, so a UTC
// "...Z" instant on a non-UTC host matches the wrong rows (the REST ?at=
// parameter is affected the same way). Re-expressing the instant in the
// server's zone - the same instant, so this is harmless once storage is
// fixed - makes the comparison line up.
//
// DELETE when the storage-layer fix (query instants converted on SQLite read
// paths) is merged: replace the call in parseTimeArg with plain `t`.
// TestPastTimeQueryOnNonUTCHost covers the behaviour either way.
func toServerZone(t time.Time) time.Time {
	return t.In(time.Local)
}

// maxRelativeDuration bounds relative time arguments. Anything older than
// this is far beyond any history retention, and without a bound a huge value
// overflows time.Duration and silently lands on some arbitrary date.
const maxRelativeDuration = 10 * 365 * 24 * time.Hour

// parseFlexibleDuration parses Go durations extended with day ("d") and week
// ("w") units, e.g. "7d", "2w", "1d12h", "36h", "1.5h".
func parseFlexibleDuration(s string) (time.Duration, error) {
	s = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), " ", "")
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	var total time.Duration
	for len(s) > 0 {
		i := 0
		for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
			i++
		}
		if i == 0 {
			return 0, fmt.Errorf("expected a number in %q", s)
		}
		num, err := strconv.ParseFloat(s[:i], 64)
		if err != nil {
			return 0, err
		}
		j := i
		for j < len(s) && s[j] >= 'a' && s[j] <= 'z' {
			j++
		}
		var unit time.Duration
		switch s[i:j] {
		case "w":
			unit = 7 * 24 * time.Hour
		case "d":
			unit = 24 * time.Hour
		case "h":
			unit = time.Hour
		case "m":
			unit = time.Minute
		case "s":
			unit = time.Second
		default:
			return 0, fmt.Errorf("unknown unit %q", s[i:j])
		}
		// Checked in float before converting: the conversion itself is what
		// overflows for absurd inputs.
		if num*float64(unit) > float64(maxRelativeDuration) {
			return 0, fmt.Errorf("duration exceeds the %d-year limit", maxRelativeDuration/(365*24*time.Hour))
		}
		total += time.Duration(num * float64(unit))
		if total > maxRelativeDuration {
			return 0, fmt.Errorf("duration exceeds the %d-year limit", maxRelativeDuration/(365*24*time.Hour))
		}
		s = s[j:]
	}
	return total, nil
}

// parseOptionalTime parses an optional "at"-style argument; empty means now
// (the zero time, which the service layer reads as "current state").
func parseOptionalTime(arg, raw string, now time.Time) (time.Time, error) {
	if strings.TrimSpace(raw) == "" || strings.EqualFold(strings.TrimSpace(raw), "now") {
		return time.Time{}, nil
	}
	return parseTimeArg(arg, raw, now)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
