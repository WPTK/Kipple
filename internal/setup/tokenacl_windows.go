//go:build windows

package setup

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// restrictToOwner gives the file at path a protected DACL that grants the
// current user full access and nobody else anything. On Windows the 0600 mode
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
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")")
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
