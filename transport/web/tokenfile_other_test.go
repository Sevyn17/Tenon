//go:build !windows

package web

import (
	"os"
	"testing"
)

func checkPrivate(t *testing.T, path string) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if m := st.Mode().Perm(); m != 0o600 {
		t.Fatalf("权限应为 0600，得 %o", m)
	}
}
