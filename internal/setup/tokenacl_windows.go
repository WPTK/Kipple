//go:build windows

package setup

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// restrictToOwner gives the file at path a protected DACL that grants full
// access to the current user, SYSTEM and the Administrators group, and nobody
// else anything. (Administrators could take ownership anyway; listing them
// keeps `kipple setup-token` working from an elevated prompt when the server
// runs as a service under another account.) On Windows the 0600 mode
// passed to os.OpenFile only toggles the read-only attribute: the file would
// otherwise inherit the data directory's ACL, which often lets every local
// user read it (C:\ProgramData, a shared folder). Called on the new, still
// empty token file, before the token is written.
func restrictToOwner(path string) error {
	tok := windows.GetCurrentProcessToken()
	user, err := tok.GetTokenUser()
	if err != nil {
		return fmt.Errorf("setup token file: current user: %w", err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FA;;;SY)(A;;FA;;;BA)")
	if err != nil {
		return fmt.Errorf("setup token file: security descriptor: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("setup token file: DACL: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("setup token file: set owner-only access: %w", err)
	}
	return nil
}
