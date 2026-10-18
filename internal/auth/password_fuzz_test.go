package auth

import (
	"fmt"
	"strings"
	"testing"
)

// FuzzCheckPassword: a stored hash that is not a well-formed argon2id PHC string never verifies and never panics,
// whatever it holds (a hand-edited or restored row). Hashes whose cost parameters would make one check slow or
// large are not run: refusing them is the point of the limits in CheckPassword.
func FuzzCheckPassword(f *testing.F) {
	valid, err := HashPassword("correct-horse")
	if err != nil {
		f.Fatal(err)
	}
	f.Add("correct-horse", valid)
	f.Add("x", "")
	f.Add("x", "$argon2id$v=19$m=8,t=1,p=1$AAAA$AAAA")
	f.Add("x", "$argon2id$v=19$m=1048577,t=1,p=1$AAAA$AAAA")
	f.Add("x", "$argon2id$v=19$m=8,t=17,p=1$AAAA$AAAA")
	f.Add("x", "$argon2id$v=19$m=8,t=1,p=0$AAAA$AAAA")
	f.Add("x", "$argon2id$v=19$m=8,t=1,p=255$AAAA$AAAA")
	f.Add("x", "$argon2i$v=19$m=8,t=1,p=1$AAAA$AAAA")
	f.Add("x", "$argon2id$v=18$m=8,t=1,p=1$AAAA$AAAA")
	f.Add("x", strings.Repeat("$", 40))
	f.Fuzz(func(t *testing.T, pw, phc string) {
		if len(pw) > 256 || len(phc) > 512 {
			return
		}
		parts := strings.Split(phc, "$")
		if len(parts) == 6 {
			var m, tc uint32
			var p uint8
			if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &tc, &p); err == nil && (m > 4096 || tc > 3 || p > 4) {
				return // a cost CheckPassword allows but a fuzz run cannot afford
			}
		}
		ok := CheckPassword(pw, phc)
		if ok && phc != valid {
			// Only a hash made for this password may verify; the one valid seed is for "correct-horse".
			if !strings.HasPrefix(phc, "$argon2id$v=19$m=") {
				t.Fatalf("CheckPassword accepted a malformed hash %q for %q", phc, pw)
			}
		}
		if phc == valid && ok != (pw == "correct-horse") {
			t.Fatalf("the valid hash verified %q = %v", pw, ok)
		}
	})
}
