package security

import (
	"regexp"
	"strings"
	"testing"
)

func TestPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("rahasia123")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("unexpected hash format %q", h)
	}
	if ok, err := VerifyPassword(h, "rahasia123"); err != nil || !ok {
		t.Fatalf("correct password rejected: ok=%v err=%v", ok, err)
	}
	if ok, _ := VerifyPassword(h, "rahasia124"); ok {
		t.Fatal("wrong password accepted")
	}
	h2, _ := HashPassword("rahasia123")
	if h == h2 {
		t.Fatal("two hashes of the same password must differ (random salt)")
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	for _, bad := range []string{"", "plain", "$argon2i$v=19$m=1,t=1,p=1$c2FsdA$aGFzaA", "$argon2id$v=19$m=x$a$b"} {
		if ok, err := VerifyPassword(bad, "x"); ok || err == nil {
			t.Errorf("%q: want an error, got ok=%v err=%v", bad, ok, err)
		}
	}
}

func TestTokens(t *testing.T) {
	a, b := NewToken("vmk_"), NewToken("vmk_")
	if a == b || !strings.HasPrefix(a, "vmk_") || len(a) != 4+43 {
		t.Fatalf("bad tokens %q %q", a, b)
	}
	if len(HashToken(a)) != 64 || HashToken(a) != HashToken(a) {
		t.Fatal("HashToken must be a stable 64-char hex digest")
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(NewUUID()) {
		t.Fatal("NewUUID is not a v4 UUID")
	}
	if p := TemporaryPassword(10); len(p) != 10 || strings.ContainsAny(p, "01lio") {
		t.Fatalf("bad temporary password %q", p)
	}
}
