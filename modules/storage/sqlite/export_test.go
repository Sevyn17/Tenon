package sqlite

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

func TestExportJSONL(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	c := newCore()
	if err := db.CreateSession(ctx, store.SessionMeta{ID: "s1"}, c.Snapshot().Header, nil); err != nil {
		t.Fatal(err)
	}
	n1, _ := c.Input(txt("你好"), nil)
	n := commitAll(t, db, "s1", c, 0, func(cp *store.Checkpoint) {
		cp.Request = &store.RequestRecord{RequestID: n1.Model.RequestID, ShellHash: "x", PrefixEnd: 1, FinalHash: "f"}
		cp.Spans = []store.Span{{ID: "sp1", Kind: "step", Start: time.Now()}}
	})
	_, _ = c.ModelDone(n1.Model.RequestID, n1.ID, session.AssistantItem{Content: txt("嗨")}, session.StopReason{})
	commitAll(t, db, "s1", c, n, nil)

	var buf bytes.Buffer
	if err := db.Export(ctx, "s1", &buf); err != nil {
		t.Fatalf("Export: %v", err)
	}
	kinds := map[string]int{}
	sc := bufio.NewScanner(&buf)
	for sc.Scan() {
		var line struct {
			Kind string          `json:"kind"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("非法 JSONL 行: %v %s", err, sc.Text())
		}
		kinds[line.Kind]++
	}
	if kinds["session"] != 1 || kinds["entry"] != 2 || kinds["request"] != 1 || kinds["span"] != 1 {
		t.Fatalf("导出种类不符: %v", kinds)
	}
	if err := db.Export(ctx, "nope", &buf); err != store.ErrNotFound {
		t.Fatalf("未知会话应 ErrNotFound: %v", err)
	}
}
