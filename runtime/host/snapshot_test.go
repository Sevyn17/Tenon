package host

// 回归快照基线（框架设计 09 9.2，T1a）：用假模型走几段典型对话，把每次调模型的请求
// 规范化后与 testdata/snapshot_requests.json 逐字节比较。之后每一步改动都靠它发现
// "模型看到的内容变了"。有意变化时用 `go test ./core/host -run TestRequestSnapshot -update` 重录。

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"ybase/runtime/session"
)

var update = flag.Bool("update", false, "重录回归快照")

// normalize 把内核生成的随机 id 换成按出现顺序编号，其余原样保留。
func normalize(reqs []session.Request) []session.Request {
	ids := map[string]string{}
	id := func(s string) string {
		if s == "" {
			return ""
		}
		if v, ok := ids[s]; ok {
			return v
		}
		v := fmt.Sprintf("id%d", len(ids)+1)
		ids[s] = v
		return v
	}
	out := make([]session.Request, len(reqs))
	for i, r := range reqs {
		b, _ := json.Marshal(r)
		var c session.Request
		_ = json.Unmarshal(b, &c)
		for j := range c.Messages {
			m := &c.Messages[j]
			m.ToolCallID = id(m.ToolCallID)
			for k := range m.ToolCalls {
				m.ToolCalls[k].ID = id(m.ToolCalls[k].ID)
				m.ToolCalls[k].WireID = id(m.ToolCalls[k].WireID)
			}
		}
		out[i] = c
	}
	return out
}

func TestRequestSnapshot(t *testing.T) {
	ctx := context.Background()
	m := &fakeModel{}
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), m)
	hd, err := r.h.Open(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	// 1. 补报 + 普通对话
	if err := hd.Do(Command{Kind: Note, ItemID: "n1", Text: "后台任务完成"}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Do(Command{Kind: Send, ItemID: "i1", Text: "你好"}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	// 2. 工具调用 + 人工确认
	if err := hd.Do(Command{Kind: Send, ItemID: "i2", Text: "用 tool"}); err != nil {
		t.Fatal(err)
	}
	callID := waitConfirm(t, r.db, "s1")
	waitFor(t, func() bool { return hd.Do(Command{Kind: Confirm, CallID: callID, Approve: true}) == nil })
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	// 3. 再来一轮普通对话（带完整历史）
	if err := hd.Do(Command{Kind: Send, ItemID: "i3", Text: "谢谢"}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}

	m.mu.Lock()
	got, err := json.MarshalIndent(normalize(m.reqs), "", "  ")
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "snapshot_requests.json")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("缺快照，先用 -update 录制: %v", err)
	}
	if string(want) != string(got) {
		t.Fatalf("模型请求与快照不同（有意变化用 -update 重录）\n--- 现在 ---\n%s", got)
	}
}
