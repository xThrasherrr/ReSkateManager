package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters (OWASP's 19 MiB / 2 passes / 1 lane baseline, a little stronger).
const (
	argonTime    = 2
	argonMemory  = 32 * 1024
	argonThreads = 1
	argonKeyLen  = 32
)

// hashSlots caps how many argon2 hashes run at once. Each takes argonMemory,
// and a flood of sign-ins must not take the servers' memory with it.
var hashSlots = make(chan struct{}, max(1, min(4, runtime.NumCPU())))

func idKey(pw, salt []byte, t, m uint32, p uint8, n uint32) []byte {
	hashSlots <- struct{}{}
	defer func() { <-hashSlots }()
	return argon2.IDKey(pw, salt, t, m, p, n)
}

// HashPassword hashes pw with argon2id in the PHC string format.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := idKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// CheckPassword reports whether pw matches a hash from HashPassword. The
// hash's parameters are bounded, so a hash edited in the database cannot make
// a sign-in take all the memory or panic.
func CheckPassword(hash, pw string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	if m < 8*uint32(p) || m > 256*1024 || t < 1 || t > 16 || p < 1 || p > 16 {
		return false
	}
	b64 := base64.RawStdEncoding
	salt, err1 := b64.DecodeString(parts[4])
	want, err2 := b64.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(salt) < 8 || len(want) < 16 || len(want) > 64 {
		return false
	}
	got := idKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// ValidatePassword enforces the length rule for new passwords.
func ValidatePassword(pw string) error {
	if len(pw) < 10 {
		return errors.New("passwords must be at least 10 characters")
	}
	if len(pw) > 256 {
		return errors.New("passwords must be at most 256 characters")
	}
	return nil
}

// dummyHash makes a failed lookup cost as much as a wrong password.
var dummyHash, _ = HashPassword("not a real password")
