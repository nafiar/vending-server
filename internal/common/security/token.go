package security

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
)

// NewToken returns prefix + 32 random bytes in URL-safe base64 (256 bits).
func NewToken(prefix string) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("security: crypto/rand failed: %v", err)) // never happens on supported platforms
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

// HashToken is how session tokens and machine keys are stored. They are 256-bit
// random values, so a fast SHA-256 is enough and lets the server look them up
// by an index; slow hashes such as Argon2id are for low-entropy passwords.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NewUUID returns a random (version 4) UUID in lowercase.
func NewUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("security: crypto/rand failed: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// temporaryAlphabet leaves out characters that are easy to misread: 0/o, 1/l/i.
const temporaryAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// TemporaryPassword returns a random password of n characters for a reset.
func TemporaryPassword(n int) string {
	out := make([]byte, n)
	max := big.NewInt(int64(len(temporaryAlphabet)))
	for i := range out {
		k, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(fmt.Sprintf("security: crypto/rand failed: %v", err))
		}
		out[i] = temporaryAlphabet[k.Int64()]
	}
	return string(out)
}
