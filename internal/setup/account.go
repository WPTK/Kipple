package setup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/WPTK/kipple/internal/auth"
	"github.com/WPTK/kipple/internal/store"
)

// ExamplePassword is the placeholder an older .env.example shipped; it is
// refused everywhere a password is chosen, so a copied example never becomes a
// real password.
const ExamplePassword = "change-me"

// MaxUsernameLen is the longest user name (the account table's CHECK).
const MaxUsernameLen = 64

// ValidUsername is the account table's rule: 1 to 64 of A-Z a-z 0-9 . _ -.
func ValidUsername(s string) bool {
	if s == "" || len(s) > MaxUsernameLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// CheckPassword applies the account length rules (min to auth.MaxPasswordLen
// bytes) and refuses the example placeholder. name labels the error (an
// environment variable, or "the password").
func CheckPassword(name, pw string, min int) error {
	if pw == ExamplePassword {
		return fmt.Errorf("%s is the example value %q: choose a real password", name, ExamplePassword)
	}
	if n := len(pw); n < min || n > auth.MaxPasswordLen {
		return fmt.Errorf("%s must be %d to %d characters (it is %d)", name, min, auth.MaxPasswordLen, n)
	}
	return nil
}

// NewSecret is the 32 random bytes (hex) that key Reader tokens, the login memo
// and image signatures.
func NewSecret() (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	return hex.EncodeToString(secret), nil
}

// NewAccount is what CreateAccount needs. Passwords are plain text and are
// hashed here; the caller has already checked them (CheckPassword). An empty
// Password is an account without a web password: with AuthMode standard that
// is the Cloudflare Access account of design §7.0, which the caller may only
// create after verifying Access for the request; with AuthMode open it is open
// mode, which the caller may only create after the open gate passed.
type NewAccount struct {
	Username    string
	Password    string
	APIPassword string // "" = the Reader API stays disabled
	AuthMode    string // store.AuthStandard or store.AuthOpen
	CreatedVia  string // store.CreatedViaEnv or store.CreatedViaWizard
}

// ErrBadUsername is CreateAccount's answer to a user name ValidUsername refuses.
var ErrBadUsername = errors.New("the user name must be 1-64 characters of A-Z a-z 0-9 . _ -")

// CreateAccount inserts the one account row unless it exists (then created is
// false and nothing changes: of two racing callers exactly one wins). It is the
// single creation path for the environment and the wizard.
func CreateAccount(ctx context.Context, db *store.DB, a NewAccount) (created bool, acct store.Account, err error) {
	if !ValidUsername(a.Username) {
		return false, store.Account{}, ErrBadUsername
	}
	switch a.AuthMode {
	case store.AuthStandard:
	case store.AuthOpen:
		if a.Password != "" {
			return false, store.Account{}, errors.New("setup: open mode has no web password")
		}
	default:
		return false, store.Account{}, fmt.Errorf("setup: unknown auth mode %q", a.AuthMode)
	}
	var pwHash, apiHash string
	if a.Password != "" {
		if pwHash, err = auth.HashPassword(a.Password); err != nil {
			return false, store.Account{}, err
		}
	}
	if a.APIPassword != "" {
		if apiHash, err = auth.HashPassword(a.APIPassword); err != nil {
			return false, store.Account{}, err
		}
	}
	secret, err := NewSecret()
	if err != nil {
		return false, store.Account{}, err
	}
	acct = store.Account{Username: a.Username, PasswordHash: pwHash, APIPasswordHash: apiHash, Secret: secret,
		AuthMode: a.AuthMode, CreatedVia: a.CreatedVia}
	created, err = db.CreateAccount(ctx, acct)
	if err != nil || !created {
		return created, store.Account{}, err
	}
	return true, acct, nil
}

// DisplayMode is the account's sign-in mode as the API shows it: "open",
// "access" (standard without a web password) or "password".
func DisplayMode(a store.Account) string {
	switch {
	case a.AuthMode == store.AuthOpen:
		return "open"
	case a.PasswordHash == "":
		return "access"
	}
	return "password"
}
