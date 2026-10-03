package setup

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/auth"
	"github.com/WPTK/kipple/internal/store"
)

func openStore(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(t.TempDir(), "kipple.db"), Logger: quiet})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestValidUsernameAndPassword(t *testing.T) {
	for _, u := range []string{"owner", "a", "A.b_c-9", strings.Repeat("x", 64)} {
		require.True(t, ValidUsername(u), u)
	}
	for _, u := range []string{"", "owner smith", "ówner", "a/b", strings.Repeat("x", 65), "a\x00"} {
		require.False(t, ValidUsername(u), u)
	}
	require.ErrorContains(t, CheckPassword("the password", "change-me", 5), "example value")
	require.ErrorContains(t, CheckPassword("the password", "four", 5), "5 to 256")
	require.ErrorContains(t, CheckPassword("the password", strings.Repeat("x", 257), 5), "5 to 256")
	require.NoError(t, CheckPassword("the password", "five!", 5))
}

func TestCreateAccountModes(t *testing.T) {
	ctx := context.Background()
	db := openStore(t)
	_, _, err := CreateAccount(ctx, db, NewAccount{Username: "bad name", Password: "pw-12345", AuthMode: store.AuthStandard})
	require.ErrorIs(t, err, ErrBadUsername)
	_, _, err = CreateAccount(ctx, db, NewAccount{Username: "owner", Password: "pw-12345", AuthMode: store.AuthOpen})
	require.Error(t, err, "open mode never has a password")
	_, _, err = CreateAccount(ctx, db, NewAccount{Username: "owner", AuthMode: "odd"})
	require.Error(t, err)

	created, acct, err := CreateAccount(ctx, db, NewAccount{Username: "owner", AuthMode: store.AuthOpen, CreatedVia: store.CreatedViaWizard})
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "open", DisplayMode(acct))
	got, _, _ := db.Account(ctx)
	require.Equal(t, store.AuthOpen, got.AuthMode)
	require.Equal(t, store.CreatedViaWizard, got.CreatedVia)
	require.Empty(t, got.PasswordHash)
	require.Len(t, got.Secret, 64)

	require.Equal(t, "access", DisplayMode(store.Account{AuthMode: store.AuthStandard}))
	require.Equal(t, "password", DisplayMode(store.Account{AuthMode: store.AuthStandard, PasswordHash: "h"}))
}

// Two token holders racing: exactly one account is created, the others change
// nothing (ON CONFLICT DO NOTHING under the single writer).
func TestCreateAccountRaceHasOneWinner(t *testing.T) {
	ctx := context.Background()
	db := openStore(t)
	var wins atomic.Int32
	var wg sync.WaitGroup
	names := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := "user" + string(rune('a'+i))
			created, _, err := CreateAccount(ctx, db, NewAccount{Username: name, Password: "pw-" + name, AuthMode: store.AuthStandard, CreatedVia: store.CreatedViaWizard})
			require.NoError(t, err)
			if created {
				wins.Add(1)
				names <- name
			}
		}(i)
	}
	wg.Wait()
	require.EqualValues(t, 1, wins.Load())
	winner := <-names
	acct, ok, err := db.Account(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, winner, acct.Username)
	require.True(t, auth.CheckPassword("pw-"+winner, acct.PasswordHash), "the winner's own password, not a loser's")
}
