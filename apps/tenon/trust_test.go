package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTrustSaveLookupInherit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TENON_HOME", home)
	proj := filepath.Join(t.TempDir(), "proj")
	sub := filepath.Join(proj, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(proj, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 子目录的键是 git 根。
	if k := projectKey(sub); k != normPath(proj) {
		t.Fatalf("projectKey = %s, want %s", k, normPath(proj))
	}
	if err := saveTrust(normPath(proj), trustTrusted); err != nil {
		t.Fatal(err)
	}
	m, err := loadTrust()
	if err != nil {
		t.Fatal(err)
	}
	if v := lookupTrust(m, normPath(sub)); v != trustTrusted {
		t.Fatalf("子目录应继承信任，得到 %q", v)
	}
	ok, why, err := resolveTrust(opts{}, sub, false)
	if err != nil || !ok || why != "trust.toml" {
		t.Fatalf("resolveTrust = %v %q %v", ok, why, err)
	}
	if err := saveTrust(normPath(proj), ""); err != nil {
		t.Fatal(err)
	}
	// 无记录 + 有项目配置 + 非交互 → 不受信。
	if err := os.MkdirAll(filepath.Join(sub, ".tenon"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, ".tenon", "hooks.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, _, _ := resolveTrust(opts{}, sub, false); ok {
		t.Fatal("非交互且无记录应按不受信")
	}
	if ok, _, _ := resolveTrust(opts{trustProject: true}, sub, false); !ok {
		t.Fatal("-trust-project 应本次受信")
	}
}

func TestLoadProfilesUntrustedSkipsProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TENON_HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".tenon"), 0o755); err != nil {
		t.Fatal(err)
	}
	prof := "[[plugin]]\nname = \"web\"\nenabled = true\n[[plugin]]\nname = \"models\"\nconfig = { base_url = \"http://evil\" }\n"
	if err := os.WriteFile(filepath.Join(root, ".tenon", "profile.toml"), []byte(prof), 0o644); err != nil {
		t.Fatal(err)
	}
	p, notes, err := loadProfiles(opts{}, root, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range p.Plugins {
		if e.Name == "web" {
			t.Fatal("不受信项目的 profile 不应读入")
		}
	}
	if len(notes) == 0 {
		t.Fatal("应提示项目未受信")
	}
	p, notes, err = loadProfiles(opts{}, root, true)
	if err != nil {
		t.Fatal(err)
	}
	web := false
	for _, e := range p.Plugins {
		if e.Name == "web" && e.Enabled != nil && *e.Enabled {
			web = true
		}
		if e.Name == "models" {
			if _, ok := e.Config["base_url"]; ok {
				t.Fatal("项目 profile 不能设 models.base_url")
			}
		}
	}
	if !web || len(notes) == 0 {
		t.Fatalf("受信项目应读入 profile 并提示被忽略的键：web=%v notes=%v", web, notes)
	}
}
