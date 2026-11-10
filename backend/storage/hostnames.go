package storage

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// MaxHostnames caps how many DNS names one connection carries. A CDN
	// address can legitimately be reached under dozens of names; beyond a
	// handful they stop being readable.
	MaxHostnames = 8

	// HostnameMaxAge is how long a name is kept after it was last observed.
	// A name that stops being reported (the workload stopped using it, or it
	// was wrong) ages out instead of labelling the edge forever.
	HostnameMaxAge = 7 * 24 * time.Hour

	// HostnameRefresh is the granularity of a name's last-seen time: a
	// re-observation within this long of the stored time is not a change,
	// which keeps steady traffic from rewriting the row every report.
	HostnameRefresh = 10 * time.Minute
)

// Hostname is one DNS name observed for a connection and when it was last
// observed.
type Hostname struct {
	Name     string    `json:"name"`
	LastSeen time.Time `json:"last_seen"`
}

// ObservedHostnames returns names as observations made at `at`.
func ObservedHostnames(at time.Time, names ...string) []Hostname {
	out := make([]Hostname, 0, len(names))
	for _, n := range names {
		if n != "" {
			out = append(out, Hostname{Name: n, LastSeen: at})
		}
	}
	return out
}

// MergeHostnames combines two observation lists: one entry per name with the
// later last-seen (a re-observation within HostnameRefresh keeps the older
// time, so it is not a change), names not seen within HostnameMaxAge of now
// dropped, ordered most recently seen first (then by name), capped at
// MaxHostnames. Neither input is modified. nil when nothing remains.
func MergeHostnames(newer, older []Hostname, now time.Time) []Hostname {
	if len(newer) == 0 && len(older) == 0 {
		return nil
	}
	cutoff := now.Add(-HostnameMaxAge)
	byName := make(map[string]time.Time, len(newer)+len(older))
	for _, h := range older {
		if h.Name != "" && h.LastSeen.After(byName[h.Name]) {
			byName[h.Name] = h.LastSeen
		}
	}
	for _, h := range newer {
		if h.Name == "" {
			continue
		}
		prev, ok := byName[h.Name]
		if !ok || h.LastSeen.Sub(prev) >= HostnameRefresh {
			byName[h.Name] = h.LastSeen
		}
	}
	out := make([]Hostname, 0, len(byName))
	for n, t := range byName {
		if t.After(cutoff) {
			out = append(out, Hostname{Name: n, LastSeen: t})
		}
	}
	SortHostnames(out)
	if len(out) > MaxHostnames {
		out = out[:MaxHostnames]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SortHostnames orders observations most recently seen first, then by name.
func SortHostnames(hs []Hostname) {
	sort.Slice(hs, func(i, j int) bool {
		if !hs[i].LastSeen.Equal(hs[j].LastSeen) {
			return hs[i].LastSeen.After(hs[j].LastSeen)
		}
		return hs[i].Name < hs[j].Name
	})
}

// HostnameNames returns just the names, in order.
func HostnameNames(hs []Hostname) []string {
	if len(hs) == 0 {
		return nil
	}
	out := make([]string, len(hs))
	for i, h := range hs {
		out[i] = h.Name
	}
	return out
}

// EqualHostnames reports whether two observation lists are identical (names,
// order and last-seen to the second - the stored precision).
func EqualHostnames(a, b []Hostname) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].LastSeen.Unix() != b[i].LastSeen.Unix() {
			return false
		}
	}
	return true
}

// EncodeHostnames is the column encoding shared by both backends:
// "name|unixSeconds" entries joined with ",". Hostnames are validated to the
// [a-z0-9._-] alphabet before they are stored (see service.normalizeHostname),
// so neither separator can occur inside one.
func EncodeHostnames(hs []Hostname) string {
	var b strings.Builder
	for i, h := range hs {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(h.Name)
		b.WriteByte('|')
		b.WriteString(strconv.FormatInt(h.LastSeen.Unix(), 10))
	}
	return b.String()
}

// DecodeHostnames reverses EncodeHostnames, skipping malformed entries. An
// empty column is nil.
func DecodeHostnames(s string) []Hostname {
	if s == "" {
		return nil
	}
	var out []Hostname
	for _, part := range strings.Split(s, ",") {
		name, ts, ok := strings.Cut(part, "|")
		if !ok || name == "" {
			continue
		}
		sec, err := strconv.ParseInt(ts, 10, 64)
		if err != nil {
			continue
		}
		out = append(out, Hostname{Name: name, LastSeen: time.Unix(sec, 0).UTC()})
	}
	return out
}
