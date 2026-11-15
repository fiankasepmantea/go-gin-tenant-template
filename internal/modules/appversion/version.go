package appversion

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ErrBadVersion: not a version number such as 1.2.3.
var ErrBadVersion = errors.New("appversion: not a version number")

var versionRe = regexp.MustCompile(`^[0-9]{1,6}(\.[0-9]{1,6}){0,3}$`)

// Version is a dotted number of one to four parts: 2, 1.4, 1.4.2, 1.4.2.7.
type Version []int

// ParseVersion reads "1.4.2". Letters, a leading "v", pre-release tags and empty parts are refused: a client
// that compares versions as text gets 1.10 < 1.9 wrong, so the server only deals in numbers.
func ParseVersion(s string) (Version, error) {
	s = strings.TrimSpace(s)
	if !versionRe.MatchString(s) {
		return nil, fmt.Errorf("%w: %q", ErrBadVersion, s)
	}
	parts := strings.Split(s, ".")
	v := make(Version, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("%w: %q", ErrBadVersion, s)
		}
		v[i] = n
	}
	return v, nil
}

// Compare returns -1, 0 or 1. Missing parts count as 0, so 1.2 equals 1.2.0.
func (v Version) Compare(o Version) int {
	for i := 0; i < max(len(v), len(o)); i++ {
		a, b := v.part(i), o.part(i)
		switch {
		case a < b:
			return -1
		case a > b:
			return 1
		}
	}
	return 0
}

func (v Version) part(i int) int {
	if i < len(v) {
		return v[i]
	}
	return 0
}

// String is the canonical text: at least three parts, and no trailing zero beyond the third. Two spellings of
// the same number (1.2, 1.2.0, 1.2.0.0) therefore become one text, which the unique index can compare.
func (v Version) String() string {
	n := max(len(v), 3)
	for n > 3 && v.part(n-1) == 0 {
		n--
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = strconv.Itoa(v.part(i))
	}
	return strings.Join(parts, ".")
}

// Canonical parses s and returns its canonical text.
func Canonical(s string) (string, error) {
	v, err := ParseVersion(s)
	if err != nil {
		return "", err
	}
	return v.String(), nil
}
