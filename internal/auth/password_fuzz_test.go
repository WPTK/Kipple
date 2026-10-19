package auth

import (
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
	salt, tag := "AAAAAAAAAAAAAAAAAAAAAA", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	for _, cost := range []string{
		"m=8,t=1,p=1", "m=1048577,t=1,p=1", "m=8,t=0,p=1", "m=8,t=17,p=1", "m=8,t=1,p=0", "m=8,t=1,p=255", "m=0,t=1,p=1",
		"m=8,t=1,p=2A5", "m=+8,t=1,p=1",
	} {
		f.Add("x", "$argon2id$v=19$"+cost+"$"+salt+"$"+tag)
	}
	f.Add("x", "$argon2i$v=19$m=8,t=1,p=1$"+salt+"$"+tag)
	f.Add("x", "$argon2id$v=18$m=8,t=1,p=1$"+salt+"$"+tag)
	f.Add("x", "$argon2id$v=19junk$m=8,t=1,p=1$"+salt+"$"+tag)
	f.Add("x", "0$argon2id$v=19$m=8,t=1,p=1$"+salt+"$"+tag)
	f.Add("x", strings.Repeat("$", 40))
	f.Fuzz(func(t *testing.T, pw, phc string) {
		if len(pw) > 256 || len(phc) > 512 {
			return
		}
		if h, ok := parseHash(phc); ok && (h.memory > 4096 || h.time > 3) {
			return // a cost CheckPassword allows but a fuzz run cannot afford
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
