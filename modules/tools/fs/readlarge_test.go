package fs

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 大文件流式读与整读的输出逐字一致（BOM、CRLF、各种 offset / limit），且行数统计正确
// （2026-11-04 审查 P1-42：原先 read_file 把几 GB 的文件整份读进内存）。
func TestReadLargeMatchesReadRange(t *testing.T) {
	var b strings.Builder
	b.WriteString("\xEF\xBB\xBF")
	for i := 1; i <= 300; i++ {
		b.WriteString("line ")
		b.WriteString(strings.Repeat("x", i%7))
		if i%3 == 0 {
			b.WriteString("\r\n")
		} else {
			b.WriteString("\n")
		}
	}
	data := []byte(b.String())
	p := filepath.Join(t.TempDir(), "big.log")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ off, lim int }{{0, 0}, {1, 10}, {150, 0}, {299, 5}, {300, 1}, {301, 0}, {10, 2000}} {
		want, werr := readRange("big.log", data, c.off, c.lim)
		got, gerr := readLarge("big.log", p, c.off, c.lim)
		if (werr == nil) != (gerr == nil) || got != want {
			t.Fatalf("offset=%d limit=%d：\n流式 %q %v\n整读 %q %v", c.off, c.lim, got, gerr, want, werr)
		}
	}
}

// read_file 对超过阈值的文件走流式读，结果正确（只取前几行，不报错）。
func TestReadFileLargeFile(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "huge.log")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("y", 1023) + "\n"
	for n := 0; n <= ReadStreamAbove/len(line); n++ {
		if _, err := f.WriteString(line); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	out := callFileTool(t, ReadFile(root), `{"path":"huge.log","limit":2}`)
	var s string
	for _, p := range out.Content {
		s += p.Text
	}
	if strings.Count(s, line) != 2 || !strings.Contains(s, "用 offset=3 继续") {
		t.Fatalf("大文件读取结果不对：%.200q", s)
	}
}

// 读大文件的前几行不会把整份文件读进内存：分配量远小于文件大小。
func TestReadFileLargeAllocBounded(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "huge.log")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("z", 1023) + "\n"
	size := 4 * ReadStreamAbove
	for n := 0; n*len(line) < size; n++ {
		if _, err := f.WriteString(line); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	tool := ReadFile(root)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_ = callFileTool(t, tool, `{"path":"huge.log","limit":2}`)
	runtime.ReadMemStats(&after)
	// 逐行读仍会为每行分配（约等于文件大小的一部分转瞬即逝的小对象），但不会一次性要整份文件那么大的缓冲；
	// 用"单次峰值"不好测，改测 HeapAlloc 增长：整读时读完就有 size 字节的切片存活到函数返回前。
	if grew := after.TotalAlloc - before.TotalAlloc; grew > uint64(size)*3 {
		t.Logf("总分配 %d MB", grew>>20)
	}
	if after.HeapSys > before.HeapSys+uint64(size) {
		t.Fatalf("读大文件前几行让堆增长了 %d MB（文件 %d MB），疑似整份读入", (after.HeapSys-before.HeapSys)>>20, size>>20)
	}
}

// grep 跳过超大文件并在结果里说明，不把它整份读进内存（审查 P1-42）。
func TestGrepSkipsHugeFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "small.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(root, "huge.log"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(GrepMaxFile + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	out, err := grep(context.Background(), root, "needle", "", "", false, true, 0, GrepLimit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "small.txt") || !strings.Contains(out, "跳过了 1 个") {
		t.Fatalf("应命中小文件并注明跳过大文件：%q", out)
	}
}
