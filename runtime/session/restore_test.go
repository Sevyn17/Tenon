package session

import "testing"

// 窗口内含多次压缩时，从 compaction_base 加载应能恢复（缺陷回归）。
func TestRestoreWindowWithTwoCompactions(t *testing.T) {
	h := newHarness(t, 8)
	for _, s := range []string{"a", "b", "c"} {
		if err := h.core.Record(txt(s), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.core.Compact(txt("s1"), 2, nil); err != nil {
		t.Fatal(err)
	}
	if err := h.core.Compact(txt("s2"), 3, nil); err != nil {
		t.Fatal(err)
	}
	snap := h.core.Snapshot()
	b := snap.Header.CompactionBase
	for _, base := range []int{0, b} {
		if _, err := Restore(Snapshot{Header: snap.Header, Base: base, Entries: snap.Entries[base:]}); err != nil {
			t.Fatalf("base=%d: %v", base, err)
		}
	}
}

// 压缩边界在窗口内回退应拒绝。
func TestRestoreRejectsCompactionRegression(t *testing.T) {
	h := newHarness(t, 8)
	for _, s := range []string{"a", "b", "c"} {
		_ = h.core.Record(txt(s), nil)
	}
	_ = h.core.Compact(txt("s1"), 3, nil)
	snap := h.core.Snapshot()
	snap.Entries[3].Item.Compaction.Upto = 3
	bad := *snap.Entries[3]
	bad.ID = "dup"
	c2 := bad
	cp := *bad.Item.Compaction
	cp.Upto = 2
	c2.Item.Compaction = &cp
	entries := append(append([]*Entry{}, snap.Entries...), &c2)
	hdr := snap.Header
	hdr.LogLen = len(entries)
	if _, err := Restore(Snapshot{Header: hdr, Base: 0, Entries: entries}); err == nil {
		t.Fatal("压缩边界回退应被拒绝")
	}
}
