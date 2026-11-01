// Package password hashes and verifies passwords with argon2id.
//
// Hashes are stored in the standard PHC string format, so the parameters travel with the hash:
//
//	$argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>
//
// That lets the cost be raised later: Verify reports when a stored hash was made with weaker
// parameters than the current ones, and the caller can re-hash it on the next successful login.
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// MaxLength is the longest password accepted. It bounds the work an attacker can force per request.
const MaxLength = 1024

const (
	saltLen = 16
	keyLen  = 32 // bytes; stored hashes with another length are rejected as malformed

	// Upper bounds for parameters read from a stored hash, so a corrupted or malicious row
	// cannot make the server allocate gigabytes.
	maxMemoryKiB = 1 << 20 // 1 GiB
	maxTime      = 20
	maxThreads   = 64
)

// MinLength is the shortest password accepted when a password is created or changed.
// Length matters more than composition rules (NIST SP 800-63B), so there are no "must contain a symbol" rules.
const MinLength = 10

var (
	// ErrTooLong is returned when the password is longer than MaxLength.
	ErrTooLong = errors.New("password: too long")
	// ErrTooShort is returned by Validate when the password is shorter than MinLength.
	ErrTooShort = errors.New("password: too short")
)

// Validate checks a NEW password (account creation or password change). Login does not use it:
// existing passwords are only compared, never judged.
func Validate(plain string) error {
	switch n := len([]rune(plain)); {
	case n < MinLength:
		return fmt.Errorf("%w: use at least %d characters", ErrTooShort, MinLength)
	case len(plain) > MaxLength:
		return ErrTooLong
	}
	return nil
}

// Params are the argon2id cost parameters.
type Params struct {
	MemoryKiB uint32 // memory in KiB
	Time      uint32 // iterations
	Threads   uint8  // parallelism
}

// DefaultParams follow the OWASP guidance for argon2id (at least 19 MiB, 2 iterations) with extra headroom.
// Memory is per concurrent login, so lower MemoryKiB if you expect many simultaneous logins on small machines.
func DefaultParams() Params {
	return Params{MemoryKiB: 64 * 1024, Time: 3, Threads: 2}
}

func (p Params) validate() error {
	if p.MemoryKiB < 8 || p.MemoryKiB > maxMemoryKiB {
		return fmt.Errorf("password: memory must be between 8 and %d KiB", maxMemoryKiB)
	}
	if p.Time < 1 || p.Time > maxTime {
		return fmt.Errorf("password: time must be between 1 and %d", maxTime)
	}
	if p.Threads < 1 || p.Threads > maxThreads {
		return fmt.Errorf("password: threads must be between 1 and %d", maxThreads)
	}
	return nil
}

// Hasher creates and verifies argon2id hashes.
type Hasher struct {
	params Params
	dummy  string // a valid hash used to spend the same time when the user does not exist
}

// New creates a Hasher.
func New(p Params) (*Hasher, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	h := &Hasher{params: p}
	dummy, err := h.Hash("dummy-password-for-timing-equalization")
	if err != nil {
		return nil, err
	}
	h.dummy = dummy
	return h, nil
}

// Hash returns the PHC string for the password, with a fresh random salt.
func (h *Hasher) Hash(plain string) (string, error) {
	if len(plain) > MaxLength {
		return "", ErrTooLong
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("password: read random salt: %w", err)
	}
	key := argon2.IDKey([]byte(plain), salt, h.params.Time, h.params.MemoryKiB, h.params.Threads, keyLen)
	return encode(h.params, salt, key), nil
}

// Verify reports whether plain matches the stored hash. needsRehash is true when the hash is valid
// but was made with parameters other than the current ones. An error means the stored hash is malformed;
// callers should treat that like a failed login.
func (h *Hasher) Verify(plain, encoded string) (ok, needsRehash bool, err error) {
	if len(plain) > MaxLength {
		return false, false, nil
	}
	p, salt, want, err := decode(encoded)
	if err != nil {
		return false, false, err
	}
	got := argon2.IDKey([]byte(plain), salt, p.Time, p.MemoryKiB, p.Threads, keyLen)
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, false, nil
	}
	return true, p != h.params, nil
}

// Dummy spends roughly the same time as a real Verify. Call it when the account does not exist,
// so response time does not reveal which usernames are registered.
func (h *Hasher) Dummy(plain string) {
	_, _, _ = h.Verify(plain, h.dummy)
}

func encode(p Params, salt, key []byte) string {
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.MemoryKiB, p.Time, p.Threads, b64.EncodeToString(salt), b64.EncodeToString(key))
}

var errMalformed = errors.New("password: malformed hash")

func decode(encoded string) (Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	// ["", "argon2id", "v=19", "m=..,t=..,p=..", salt, key]
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return Params{}, nil, nil, errMalformed
	}
	if parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return Params{}, nil, nil, errMalformed
	}

	var p Params
	for _, kv := range strings.Split(parts[3], ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return Params{}, nil, nil, errMalformed
		}
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return Params{}, nil, nil, errMalformed
		}
		switch k {
		case "m":
			p.MemoryKiB = uint32(n)
		case "t":
			p.Time = uint32(n)
		case "p":
			if n > maxThreads {
				return Params{}, nil, nil, errMalformed
			}
			p.Threads = uint8(n)
		default:
			return Params{}, nil, nil, errMalformed
		}
	}
	if err := p.validate(); err != nil {
		return Params{}, nil, nil, errMalformed
	}

	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return Params{}, nil, nil, errMalformed
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil || len(key) != keyLen {
		return Params{}, nil, nil, errMalformed
	}
	return p, salt, key, nil
}
