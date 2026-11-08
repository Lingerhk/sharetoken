package secfile

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// sensitiveAccess is what another account must not hold. Reading a file's
// attributes or its ACL, and waiting on it, stay allowed: they reveal nothing.
const sensitiveAccess = windows.FILE_READ_DATA | windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA |
	windows.WRITE_DAC | windows.WRITE_OWNER | windows.DELETE |
	windows.GENERIC_READ | windows.GENERIC_WRITE | windows.GENERIC_ALL

// trustedSIDs are the accounts allowed to hold access: this user, and SYSTEM
// and Administrators, which can take any file anyway (like root on Unix).
func trustedSIDs() (me *windows.SID, rest []*windows.SID, err error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, nil, err
	}
	for _, t := range []windows.WELL_KNOWN_SID_TYPE{windows.WinLocalSystemSid, windows.WinBuiltinAdministratorsSid} {
		sid, err := windows.CreateWellKnownSid(t)
		if err != nil {
			return nil, nil, err
		}
		rest = append(rest, sid)
	}
	return user.User.Sid, rest, nil
}

// restrict replaces the file's ACL with one for the trusted accounts, and stops
// it inheriting from the directory, which may be a shared one.
func restrict(path string) error {
	me, rest, err := trustedSIDs()
	if err != nil {
		return err
	}
	var entries []windows.EXPLICIT_ACCESS
	for _, sid := range append([]*windows.SID{me}, rest...) {
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		})
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

// CheckPrivate returns an error, with the command that fixes it, if the file's
// ACL gives any other account access to the file at path.
func CheckPrivate(path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	me, rest, err := trustedSIDs()
	if err != nil {
		return err
	}
	exposed := fmt.Errorf(`%s is accessible by other users; run: icacls "%s" /inheritance:r /grant:r *%s:F`, path, path, me.String())
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil { // no DACL at all grants everyone everything
		return exposed
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return err
		}
		// A deny entry grants nothing. Other types carry their SID elsewhere, and
		// no plain file has them, so they are refused rather than parsed.
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return exposed
		}
		if ace.Mask&sensitiveAccess == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		trusted := sid.Equals(me)
		for _, t := range rest {
			trusted = trusted || sid.Equals(t)
		}
		if !trusted {
			return exposed
		}
	}
	return nil
}
