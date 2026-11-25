package host

import (
	"context"
	"testing"
)

// 宿主层回退后回合号重用（Mortise docs/76 §16 D4）：三回合 a b c，回退到回合 2，再发一条，新条目回合号为 2。
func TestRewindReusesTurnNumberHost(t *testing.T) {
	r := newRig(t, nil, &fakeModel{})
	ctx := context.Background()
	hd, err := r.h.Create(ctx, "s", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	send := func(s string) {
		if err := hd.Do(Command{Kind: Send, Text: s}); err != nil {
			t.Fatal(err)
		}
		if err := hd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []string{"a", "b", "c"} {
		send(s)
	}
	before := hd.Agent().Committed().Entries
	var oldID string
	for _, e := range before {
		if e.Turn == 2 {
			oldID = e.ID
			break
		}
	}
	var to int
	if err := hd.Do(Command{Kind: Rewind, ToTurn: 2, RewoundTo: &to}); err != nil {
		t.Fatal(err)
	}
	send("d")
	es := hd.Agent().Committed().Entries
	last := es[len(es)-1]
	if last.Turn != 2 {
		t.Fatalf("回退后新回合应重用回合号 2，得到 %d", last.Turn)
	}
	for _, e := range es {
		if e.ID == oldID {
			t.Fatal("已回退的条目 id 不应再出现在日志里")
		}
	}
}
