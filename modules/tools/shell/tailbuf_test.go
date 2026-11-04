package shell

import (
	"bytes"
	"math/rand"
	"testing"
)

// 环形缓冲与"全量保留再截尾"的参考实现结果一致：任意写入序列、任意读偏移。
func TestTailBufMatchesReference(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for _, capN := range []int{1, 3, 7, 64} {
		b := &tailBuf{cap: capN}
		var all []byte
		var read int64
		for i := 0; i < 500; i++ {
			p := make([]byte, r.Intn(capN*3+1))
			for j := range p {
				p[j] = byte('a' + r.Intn(26))
			}
			if n, _ := b.Write(p); n != len(p) {
				t.Fatalf("Write 返回 %d，期望 %d", n, len(p))
			}
			all = append(all, p...)
			if r.Intn(3) == 0 {
				got, next, dropped := b.since(read)
				keep := all
				if len(keep) > capN {
					keep = keep[len(keep)-capN:]
				}
				first := int64(len(all) - len(keep))
				wantDropped := int64(0)
				from := read
				if from < first {
					wantDropped, from = first-from, first
				}
				want := all[from:]
				if got != string(want) || next != int64(len(all)) || dropped != wantDropped {
					t.Fatalf("cap=%d 第 %d 次：得 %q/%d/%d，期望 %q/%d/%d", capN, i, got, next, dropped, want, len(all), wantDropped)
				}
				read = next
			}
		}
		if !bytes.Equal([]byte(func() string { s, _, _ := b.since(0); return s }()), all[max(0, len(all)-capN):]) {
			t.Fatalf("cap=%d 最终尾部不符", capN)
		}
	}
}

// 写满之后再写不分配内存（2026-11-04 审查 P1-44：原先每次写都重分配并复制整个缓冲）。
func TestTailBufNoReallocWhenFull(t *testing.T) {
	b := &tailBuf{cap: 1 << 20}
	chunk := bytes.Repeat([]byte("x"), 4096)
	for i := 0; i < (1<<20)/4096+1; i++ {
		_, _ = b.Write(chunk)
	}
	if n := testing.AllocsPerRun(100, func() { _, _ = b.Write(chunk) }); n != 0 {
		t.Fatalf("写满后每次写分配 %v 次，期望 0", n)
	}
}
