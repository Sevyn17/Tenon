package appapi

// 按回合取条目、按回合分叉（客户端改进 M8 协议T15）：session.entries {turn}、session.fork {upto_turn}、能力 entries.turn。

import (
	"context"
	"slices"
	"strconv"
	"testing"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

// seedLong 直接在存储里建一个 600 条的会话：回合号 k 能被 3 整除时是只记录的零步回合（1 条），否则一问一答（2 条）。
// 返回每个回合的 [from, to)。
func seedLong(t *testing.T, db store.Store, id string) map[uint32][2]int {
	t.Helper()
	c := session.New(&session.Config{Model: "m", Policy: session.DefaultPolicy()})
	for k := 1; c.Snapshot().Header.LogLen < 600; k++ {
		if k%3 == 0 {
			if err := c.Record(session.Content{session.NewTextPart("记")}, nil); err != nil {
				t.Fatal(err)
			}
			continue
		}
		n, err := c.Input(session.Content{session.NewTextPart("问")}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.ModelDone(n.Model.RequestID, n.ID, session.AssistantItem{Content: session.Content{session.NewTextPart("答")}},
			session.StopReason{V: "stop"}); err != nil {
			t.Fatal(err)
		}
	}
	s := c.Snapshot()
	if s.Header.LogLen != 600 {
		t.Fatalf("应正好 600 条：%d", s.Header.LogLen)
	}
	if err := db.CreateSession(context.Background(), store.SessionMeta{Workspace: "w", ID: id}, s.Header, s.Entries); err != nil {
		t.Fatal(err)
	}
	out := map[uint32][2]int{}
	for i, e := range s.Entries {
		r, seen := out[e.Turn]
		if !seen {
			r[0] = i
		}
		r[1] = i + 1
		out[e.Turn] = r
	}
	return out
}

func entriesOf(t *testing.T, app *App, args string) (wire.EntriesResult, *Error) {
	t.Helper()
	out, e := call2(t, app, "session.entries", args)
	if e != nil {
		return wire.EntriesResult{}, e
	}
	return out.(wire.EntriesResult), nil
}

func TestEntriesTurnAndForkUptoTurn(t *testing.T) {
	app := newApp(t)
	db := app.o.DB
	turns := seedLong(t, db, "long")
	var last uint32
	for k := range turns {
		last = max(last, k)
	}
	if !slices.Contains(app.capabilities(), "entries.turn") || !slices.Contains(app.capabilities(), "model_retrying") {
		t.Fatalf("应报 entries.turn 与 model_retrying：%v", app.capabilities())
	}
	// 存储实现 TurnRangeStore（memstore）与没实现（扫条目兜底）结果一致。
	for name, st := range map[string]store.Store{"TurnRangeStore": db, "scan": plainStore{db}} {
		app.o.DB = st
		for _, k := range []uint32{1, 3, 4, 200, last} {
			r, e := entriesOf(t, app, `{"session":"long","turn":`+itoa(int(k))+`}`)
			if e != nil {
				t.Fatalf("%s：回合 %d：%+v", name, k, e)
			}
			want := turns[k]
			if len(r.Entries) != want[1]-want[0] || r.Entries[0].Idx != want[0] || r.Len != 600 {
				t.Fatalf("%s：回合 %d 期望 [%d,%d)，得到 %d 条，首条 idx=%d len=%d", name, k, want[0], want[1], len(r.Entries), r.Entries[0].Idx, r.Len)
			}
			for _, x := range r.Entries {
				if x.Turn != k {
					t.Fatalf("%s：回合 %d 里混进了回合 %d 的条目", name, k, x.Turn)
				}
			}
		}
		if w := turns[3]; w != [2]int{4, 5} {
			t.Fatalf("第 3 回合应是 [4,5)（零步回合）：%v", w)
		}
		for _, args := range []string{`{"session":"long","turn":0}`, `{"session":"long","turn":` + itoa(int(last+1)) + `}`,
			`{"session":"long","turn":3,"tail":5}`, `{"session":"long","turn":3,"from":0}`} {
			if _, e := entriesOf(t, app, args); e == nil || e.Code != wire.CodeInvalid {
				t.Fatalf("%s：%s 应为 invalid：%+v", name, args, e)
			}
		}
	}
	app.o.DB = db

	fork := func(args string) (wire.SessionSnapshot, *Error) {
		t.Helper()
		out, e := call2(t, app, "session.fork", args)
		if e != nil {
			return wire.SessionSnapshot{}, e
		}
		return out.(wire.SessionSnapshot), nil
	}
	// upto_turn = N 与 upto = 第 N 回合首条下标等价。
	for _, k := range []uint32{1, 2, 3, 100} {
		a, e := fork(`{"session":"long","to":"ft` + itoa(int(k)) + `","upto_turn":` + itoa(int(k)) + `}`)
		if e != nil {
			t.Fatalf("upto_turn=%d：%+v", k, e)
		}
		b, e := fork(`{"session":"long","to":"fu` + itoa(int(k)) + `","upto":` + itoa(turns[k][0]) + `}`)
		if e != nil {
			t.Fatalf("upto=%d：%+v", turns[k][0], e)
		}
		if a.Log.Len != turns[k][0] || a.Log != b.Log {
			t.Fatalf("upto_turn=%d：长度 %+v，等价 upto 的 %+v，期望 %d", k, a.Log, b.Log, turns[k][0])
		}
		ea, _ := entriesOf(t, app, `{"session":"ft`+itoa(int(k))+`"}`)
		eb, _ := entriesOf(t, app, `{"session":"fu`+itoa(int(k))+`"}`)
		if len(ea.Entries) != len(eb.Entries) {
			t.Fatalf("upto_turn=%d 与等价 upto 的条目数不同：%d vs %d", k, len(ea.Entries), len(eb.Entries))
		}
		for i := range ea.Entries {
			if ea.Entries[i].Turn != eb.Entries[i].Turn || ea.Entries[i].Item.Kind != eb.Entries[i].Item.Kind {
				t.Fatalf("upto_turn=%d 第 %d 条不同", k, i)
			}
		}
	}
	// 最后一回合 + 1：整会话分叉。
	if s, e := fork(`{"session":"long","to":"fall","upto_turn":` + itoa(int(last+1)) + `}`); e != nil || s.Log.Len != 600 {
		t.Fatalf("upto_turn = 最后一回合 + 1 应分叉整个会话：%+v %+v", s.Log, e)
	}
	for _, args := range []string{`{"session":"long","to":"x1","upto_turn":0}`,
		`{"session":"long","to":"x2","upto_turn":` + itoa(int(last+2)) + `}`,
		`{"session":"long","to":"x3","upto_turn":3,"upto":4}`} {
		if _, e := fork(args); e == nil || e.Code != wire.CodeInvalid {
			t.Fatalf("%s 应为 invalid：%+v", args, e)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
