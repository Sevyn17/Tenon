package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 穿插项 8：令牌写进只本人可读的文件，覆盖旧文件，退出时删除。
func TestWriteTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", TokenFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("旧内容，比新内容长得多得多得多得多得多得多得多得多得多得多\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	url := "http://127.0.0.1:1/?token=abc"
	remove, err := WriteTokenFile(path, url)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(b)) != url {
		t.Fatalf("内容应只有新地址，得 %q", b)
	}
	checkPrivate(t, path)
	remove()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("退出后应删除，stat: %v", err)
	}
}
