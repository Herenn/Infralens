package dnscache

// Limits from RFC 1035 section 2.3.4, applied to the presentation form
// without the trailing root dot.
const (
	maxNameLen  = 253
	maxLabelLen = 63
)

// NormalizeName returns the canonical reporting form of a DNS name: ASCII
// lowercase, without the trailing root dot. ok is false for anything that is
// not a plausible hostname - empty or root names, over-long names or labels,
// empty labels, any byte outside letters, digits, '-' and '_', or a final
// label that is all digits (no TLD is numeric; this is what rejects IPv4
// literals such as "52.1.2.3" posing as names).
//
// Being strict here is deliberate. Names come straight off the network and
// end up in a UI, Mermaid/DOT exports and webhook messages; restricting them
// to the hostname alphabet means no consumer ever has to escape one.
// Internationalized names arrive in their ASCII (xn--) form and pass.
func NormalizeName(s string) (string, bool) {
	var buf [maxNameLen + 1]byte
	n, ok := normalizeInto(buf[:], s)
	if !ok {
		return "", false
	}
	return string(buf[:n]), true
}

// normalizeInto writes the normalized form of src into dst (which must hold
// at least maxNameLen+1 bytes) without allocating, returning its length.
func normalizeInto[T ~string | ~[]byte](dst []byte, src T) (int, bool) {
	if len(src) > 0 && src[len(src)-1] == '.' {
		src = src[:len(src)-1]
	}
	if len(src) == 0 || len(src) > maxNameLen || len(dst) < len(src) {
		return 0, false
	}

	labelLen := 0
	labelNumeric := true
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case c >= 'a' && c <= 'z', c == '-', c == '_':
			labelNumeric = false
		case c >= '0' && c <= '9':
		case c >= 'A' && c <= 'Z':
			c += 'a' - 'A'
			labelNumeric = false
		case c == '.':
			if labelLen == 0 {
				return 0, false // empty label: leading dot or ".."
			}
			labelLen = 0
			labelNumeric = true
			dst[i] = c
			continue
		default:
			return 0, false
		}
		labelLen++
		if labelLen > maxLabelLen {
			return 0, false
		}
		dst[i] = c
	}
	if labelLen == 0 || labelNumeric {
		return 0, false
	}
	return len(src), true
}
