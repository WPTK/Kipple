//go:build windows

package setup

import (
	"testing"
	"unsafe"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// On Windows the 0600 mode is ignored, so the token file gets a protected DACL:
// the user Kipple runs as, SYSTEM and Administrators, nobody else (no Users,
// no Everyone, nothing inherited).
func TestTokenFileIsOwnerOnlyOnWindows(t *testing.T) {
	dir := t.TempDir()
	want, err := NewToken()
	require.NoError(t, err)
	require.NoError(t, writeTokenFile(dir, want))

	sd, err := windows.GetNamedSecurityInfo(TokenPath(dir), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	require.NoError(t, err)
	ctl, _, err := sd.Control()
	require.NoError(t, err)
	require.NotZero(t, ctl&windows.SE_DACL_PROTECTED, "nothing inherited from the data directory")
	dacl, _, err := sd.DACL()
	require.NoError(t, err)
	require.EqualValues(t, 3, dacl.AceCount)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	require.NoError(t, err)
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	require.NoError(t, err)
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	require.NoError(t, err)
	var sawUser bool
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		require.NoError(t, windows.GetAce(dacl, i, &ace))
		require.EqualValues(t, windows.ACCESS_ALLOWED_ACE_TYPE, ace.Header.AceType)
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		sawUser = sawUser || sid.Equals(user.User.Sid)
		require.True(t, sid.Equals(user.User.Sid) || sid.Equals(system) || sid.Equals(admins), "unexpected entry for %s", sid)
	}
	require.True(t, sawUser, "the current user has access")

	// The owner can still read it back (kipple setup-token).
	tok, ok, err := ReadToken(dir)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, want, tok)
}
