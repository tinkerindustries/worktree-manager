//go:build windows

package platform

// sec_windows.go is the Windows permission model (08-platform.md §4.6):
// mode bits are meaningless on NTFS, so private state — the store
// directory, holding wt.db and endpoint.json — is protected with a
// protected ACL whose only entry is the current user, built from an SDDL
// string and applied with the raw Win32 API through the standard
// library's syscall package (no x/sys, no cgo: CGO_ENABLED=0 throughout).
//
// The ACL used to carry more than this. While the transport was a named
// pipe, the pipe's own ACL was what identified a host client, because
// peer credentials do not exist on one. The transport is HTTP on loopback
// now and identity is the bearer token in endpoint.json, so the ACL's job
// is narrower but no less load-bearing: it is what keeps that token
// unreadable by the machine's other users, and it is the only thing that
// does. Where the ACL cannot be set, EnsurePrivateDir refuses to hold
// credentials rather than degrading.

import (
	"fmt"
	"syscall"
	"unsafe"
)

// Windows permission and pipe constants the standard library does not
// carry. Values are the stable Win32 ones.
const (
	winERROR_ACCESS_DENIED       = syscall.Errno(5)
	winERROR_PIPE_BUSY           = syscall.Errno(231)
	winERROR_NO_DATA             = syscall.Errno(232)
	winERROR_PIPE_NOT_CONNECTED  = syscall.Errno(233)
	winERROR_PIPE_CONNECTED      = syscall.Errno(535)
	winPIPE_ACCESS_DUPLEX        = 0x00000003
	winFILE_FLAG_FIRST_PIPE_INST = 0x00020000
	winPIPE_UNLIMITED_INSTANCES  = 255
	winDACL_SECURITY_INFORMATION = 0x00000004
	winPROTECTED_DACL_SEC_INFO   = 0x80000000
	winSDDL_REVISION_1           = 1
)

// userSID returns the current user's SID in string form (S-1-5-...), the
// subject of every ACL this package sets. The token comes from the
// standard library's security helpers (OpenCurrentProcessToken /
// GetTokenUser), the string from ConvertSidToStringSidW.
func userSID() (string, error) {
	tok, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return "", fmt.Errorf("opening the process token: %w", err)
	}
	defer tok.Close()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("reading the token's user: %w", err)
	}
	sid, err := tu.User.Sid.String()
	if err != nil {
		return "", fmt.Errorf("rendering the user SID: %w", err)
	}
	return sid, nil
}

// secureSecurityAttributes builds the SECURITY_ATTRIBUTES for the private
// state directory: a protected DACL whose only entry grants the current
// user GENERIC_ALL, inherited by the files and directories created inside
// it. Protected is what does the excluding — it detaches the object from
// its parent's inheritable ACEs, so an ACE that is not written here does
// not apply, and the single allow entry is therefore the whole of the
// access anyone has (docs/ARCHITECTURE.md §12.2).
//
// There is deliberately no deny-Everyone entry. Windows evaluates deny
// ACEs before allow ACEs and the current user is a member of Everyone, so
// "deny Everyone, allow me" denies the owner its own store: every open
// under the directory fails with ERROR_ACCESS_DENIED, which is what
// EnsurePrivateDir's write probe reports. Exclusion on Windows is
// expressed by the protected flag and the absence of an ACE, never by
// denying the world.
//
// The descriptor must be freed with freeSecurityDescriptor when the
// attributes are no longer needed.
func secureSecurityAttributes() (*syscall.SecurityAttributes, error) {
	sid, err := userSID()
	if err != nil {
		return nil, err
	}
	sddl := "D:P(A;OICI;GA;;;" + sid + ")"
	p, err := syscall.UTF16PtrFromString(sddl)
	if err != nil {
		return nil, fmt.Errorf("encoding the security descriptor string: %w", err)
	}
	advapi32 := syscall.NewLazyDLL("advapi32.dll")
	convert := advapi32.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	var sd uintptr
	r1, _, e1 := convert.Call(uintptr(unsafe.Pointer(p)), winSDDL_REVISION_1, uintptr(unsafe.Pointer(&sd)), 0)
	if r1 == 0 {
		return nil, fmt.Errorf("building the security descriptor: %w", e1)
	}
	return &syscall.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(syscall.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}, nil
}

// freeSecurityDescriptor releases a descriptor built by
// secureSecurityAttributes (LocalFree, the documented release for
// ConvertStringSecurityDescriptorToSecurityDescriptor).
func freeSecurityDescriptor(sa *syscall.SecurityAttributes) {
	if sa != nil && sa.SecurityDescriptor != 0 {
		syscall.LocalFree(syscall.Handle(sa.SecurityDescriptor))
	}
}

// setPrivateACL applies the private ACL (current user only) to the
// directory at path: SetFileSecurityW with the DACL and protected-DACL
// information classes, then a read-back check that the DACL actually
// landed. The read-back is the refusal's teeth: a filesystem that does
// not store ACLs (or a call that silently no-ops) is detected here,
// before any credential is written (08-platform.md §4.6).
func setPrivateACL(path string) error {
	sa, err := secureSecurityAttributes()
	if err != nil {
		return err
	}
	defer freeSecurityDescriptor(sa)
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("encoding the path: %w", err)
	}
	advapi32 := syscall.NewLazyDLL("advapi32.dll")
	set := advapi32.NewProc("SetFileSecurityW")
	r1, _, e1 := set.Call(uintptr(unsafe.Pointer(p)),
		winDACL_SECURITY_INFORMATION|winPROTECTED_DACL_SEC_INFO,
		sa.SecurityDescriptor)
	if r1 == 0 {
		return fmt.Errorf("setting the private ACL on %s: %w", path, e1)
	}
	if err := verifyPrivateACL(path); err != nil {
		return err
	}
	return nil
}

// verifyPrivateACL reads the DACL back from path and confirms it is
// present and non-empty — the check that a SetFileSecurityW success was
// actually stored by the filesystem.
func verifyPrivateACL(path string) error {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("encoding the path: %w", err)
	}
	get := syscall.NewLazyDLL("advapi32.dll").NewProc("GetFileSecurityW")
	getDacl := syscall.NewLazyDLL("advapi32.dll").NewProc("GetSecurityDescriptorDacl")
	var needed uint32
	r1, _, _ := get.Call(uintptr(unsafe.Pointer(p)), winDACL_SECURITY_INFORMATION, 0, 0, uintptr(unsafe.Pointer(&needed)))
	if r1 == 0 {
		// First call with a nil buffer fails with ERROR_INSUFFICIENT_BUFFER
		// and reports the size — that is the expected probe.
	}
	buf := make([]byte, needed)
	r1, _, e1 := get.Call(uintptr(unsafe.Pointer(p)), winDACL_SECURITY_INFORMATION,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&needed)))
	if r1 == 0 {
		return fmt.Errorf("reading the ACL of %s back: %w", path, e1)
	}
	var present, daclPresent, daclDefaulted uint32
	var dacl uintptr
	r1, _, e1 = getDacl.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&present)), uintptr(unsafe.Pointer(&dacl)), uintptr(unsafe.Pointer(&daclDefaulted)))
	if r1 == 0 {
		return fmt.Errorf("parsing the ACL of %s: %w", path, e1)
	}
	_ = daclPresent
	if present == 0 || dacl == 0 {
		return fmt.Errorf("the ACL of %s could not be verified: the read-back found no protected DACL (the filesystem may not store ACLs)", path)
	}
	return nil
}
