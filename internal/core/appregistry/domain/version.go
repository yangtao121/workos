package domain

import (
	"strconv"
	"strings"
)

// Version is the SemVer subset accepted by the canonical manifest schema
// (`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`) with identifiers additionally required
// to be non-empty so that precedence is total.
type Version struct {
	Major      uint64
	Minor      uint64
	Patch      uint64
	Prerelease []string
}

// ParseVersion parses a schema-compatible version string. It rejects empty
// prerelease identifiers (for example `1.0.0-.rc1` or `1.0.0-`) even though
// the schema pattern alone permits them, because such versions have no total
// precedence.
func ParseVersion(value string) (Version, bool) {
	release := value
	prerelease := ""
	hasPrerelease := false
	if index := strings.IndexByte(value, '-'); index >= 0 {
		release, prerelease = value[:index], value[index+1:]
		hasPrerelease = true
	}
	parts := strings.Split(release, ".")
	if len(parts) != 3 {
		return Version{}, false
	}
	parsed := Version{}
	for i, target := range []*uint64{&parsed.Major, &parsed.Minor, &parsed.Patch} {
		number, err := strconv.ParseUint(parts[i], 10, 64)
		if err != nil || (len(parts[i]) > 1 && parts[i][0] == '0') {
			return Version{}, false
		}
		*target = number
	}
	if hasPrerelease {
		if prerelease == "" {
			return Version{}, false
		}
		for _, identifier := range strings.Split(prerelease, ".") {
			if identifier == "" {
				return Version{}, false
			}
			if isNumericIdentifier(identifier) {
				if _, err := strconv.ParseUint(identifier, 10, 64); err != nil || (len(identifier) > 1 && identifier[0] == '0') {
					return Version{}, false
				}
			} else if !isAlphanumericIdentifier(identifier) {
				return Version{}, false
			}
			parsed.Prerelease = append(parsed.Prerelease, identifier)
		}
	}
	return parsed, true
}

// CompareVersion orders versions by SemVer precedence: release beats the
// corresponding prerelease, numeric identifiers compare numerically and rank
// below alphanumeric ones, and a longer identifier list wins on equal prefix.
func CompareVersion(a, b Version) int {
	for _, pair := range [][2]uint64{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a.Prerelease) == 0 && len(b.Prerelease) == 0:
		return 0
	case len(a.Prerelease) == 0:
		return 1
	case len(b.Prerelease) == 0:
		return -1
	}
	for i := 0; i < len(a.Prerelease) && i < len(b.Prerelease); i++ {
		left, right := a.Prerelease[i], b.Prerelease[i]
		leftNumeric, rightNumeric := isNumericIdentifier(left), isNumericIdentifier(right)
		switch {
		case leftNumeric && rightNumeric:
			leftNumber := mustParseUint(left)
			rightNumber := mustParseUint(right)
			if leftNumber != rightNumber {
				if leftNumber < rightNumber {
					return -1
				}
				return 1
			}
		case leftNumeric:
			return -1
		case rightNumeric:
			return 1
		default:
			if left != right {
				if left < right {
					return -1
				}
				return 1
			}
		}
	}
	switch {
	case len(a.Prerelease) == len(b.Prerelease):
		return 0
	case len(a.Prerelease) < len(b.Prerelease):
		return -1
	default:
		return 1
	}
}

func isNumericIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func isAlphanumericIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '-':
		default:
			return false
		}
	}
	return true
}

func mustParseUint(value string) uint64 {
	number, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0
	}
	return number
}
