package host

import (
	"sync"
	"testing"
)

// 缺省定义运行期可改（U2 G1）：并发读写不竞争（-race），写后读到新值。
func TestDefaultDefConcurrent(t *testing.T) {
	h := &Host{}
	if got := h.DefaultDef(); got != "" {
		t.Fatalf("零值应为空，得 %q", got)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if j%2 == 0 {
					h.SetDefaultDef("main")
				} else {
					h.SetDefaultDef("copy")
				}
			}
		}(i)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if v := h.DefaultDef(); v != "" && v != "main" && v != "copy" {
					t.Errorf("读到意外值 %q", v)
				}
			}
		}()
	}
	wg.Wait()
	h.SetDefaultDef("reviewer2")
	if got := h.DefaultDef(); got != "reviewer2" {
		t.Fatalf("写后读 = %q", got)
	}
}
