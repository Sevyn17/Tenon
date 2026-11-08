//go:build windows

package web

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// checkPrivate DACL 受保护（不继承）且只有一条给当前用户的允许项。
func checkPrivate(t *testing.T, path string) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	ctl, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	if ctl&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("DACL 应断开继承")
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if acl.AceCount != 1 {
		t.Fatalf("应只有 1 条 ACE，得 %d", acl.AceCount)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(acl, 0, &ace); err != nil {
		t.Fatal(err)
	}
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !sid.Equals(u.User.Sid) {
		t.Fatalf("ACE 应给当前用户，得 %s", sid)
	}
}
