package apphost

// 旧库并入的断言打在登记为 service:db 的句柄上，不打在 DB() 的返回值上
// （2026-11-13：v0.3.0 打在返回值上，产品的 Import 写在句柄上，断言静默失败，并入从不发生）。
// 这个测试不依赖任何存储实现，断言打错位置就会失败。

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Sevyn17/tenon/runtime/appid"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

type legacyFakeHandle struct {
	store.Store // DB() 的返回值不实现 LegacyImporter
	path        string
	got         string
	sessions    int
}

func (h *legacyFakeHandle) DB() store.Store          { return h.Store }
func (h *legacyFakeHandle) Blobs() toolhub.Artifacts { return nil }
func (h *legacyFakeHandle) Path() string             { return h.path }
func (h *legacyFakeHandle) Import(_ context.Context, path, _ string) (LegacyImportResult, error) {
	h.got = path
	return LegacyImportResult{Sessions: h.sessions}, nil
}

func TestImportLegacyAssertsOnHandle(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(appid.Project(root), "state.db")
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cur, err := os.CreateTemp(t.TempDir(), "cur-*.db")
	if err != nil {
		t.Fatal(err)
	}
	cur.Close()

	h := &legacyFakeHandle{path: cur.Name(), sessions: 3}
	r := &Runtime{Root: root, Rt: &Assembled{Store: h}}
	notes := r.importLegacy(context.Background())

	if h.got != old {
		t.Fatalf("并入没调用句柄的 Import（断言大概打回了 DB() 的返回值）：got %q", h.got)
	}
	if len(notes) != 1 || notes[0] == "" {
		t.Fatalf("应提示并入：%v", notes)
	}
	if _, err := os.Stat(old); err == nil {
		t.Fatal("旧库应已改名")
	}
	if _, err := os.Stat(old + LegacyImportedSuffix); err != nil {
		t.Fatalf("旧库应改名为 %s：%v", LegacyImportedSuffix, err)
	}
}
