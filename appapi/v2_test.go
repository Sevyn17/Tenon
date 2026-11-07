package appapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"tenon/appapi/wire"
	"tenon/runtime/host"
)

// memArt 测试用工件存储。
type memArt struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (a *memArt) Put(_ context.Context, _, _ string, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	h := hex.EncodeToString(sum[:])
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.m == nil {
		a.m = map[string][]byte{}
	}
	a.m[h] = append([]byte(nil), data...)
	return h, nil
}

func (a *memArt) Get(h string) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	d, ok := a.m[h]
	if !ok {
		return nil, errors.New("不存在")
	}
	return d, nil
}

func call2(t *testing.T, app *App, cmd, args string) (any, *Error) {
	t.Helper()
	out, err := app.Call(context.Background(), cmd, json.RawMessage(args))
	if err == nil {
		return out, nil
	}
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("%s：错误不是 *Error：%v", cmd, err)
	}
	return nil, e
}

func wantCode(t *testing.T, e *Error, code, reason string) {
	t.Helper()
	if e == nil || e.Code != code {
		t.Fatalf("应为 %s，得到 %+v", code, e)
	}
	if reason != "" && (e.Details == nil || e.Details["reason"] != reason) {
		t.Fatalf("details.reason 应为 %s：%+v", reason, e.Details)
	}
}

// G1：任何命令都不隐式新建会话。
func TestV2NoImplicitCreate(t *testing.T) {
	app := newApp(t)
	ctx := context.Background()
	for _, c := range []struct{ cmd, args string }{
		{"session.get", `{"session":"ghost"}`},
		{"session.send", `{"session":"ghost","text":"hi","client_id":"c1"}`},
		{"session.steer", `{"session":"ghost","text":"hi","client_id":"c1"}`},
		{"session.cancel", `{"session":"ghost"}`},
		{"session.rewind", `{"session":"ghost","to_turn":1}`},
		{"session.settings", `{"session":"ghost","max_steps":3}`},
		{"session.stats", `{"session":"ghost"}`},
		{"session.fork", `{"session":"ghost","to":"g2"}`},
	} {
		_, e := call2(t, app, c.cmd, c.args)
		wantCode(t, e, wire.CodeNotFound, "")
	}
	if ok, _ := app.DB().Exists(ctx, "ghost"); ok {
		t.Fatal("protocol 2 命令不应新建会话")
	}
}

// G2：closed、stale、exists、cursor_expired 各有自己的码。
func TestV2ErrorCodes(t *testing.T) {
	wantCode(t, AsError(host.ErrClosed), wire.CodeClosed, "")
	wantCode(t, AsError(fmt.Errorf("x: %w", host.ErrStale)), wire.CodeConflict, wire.ConflictStale)
	wantCode(t, AsError(host.ErrBusy), wire.CodeBusy, "")
	// 已归类过的错误再归类结果不变（传输层、bind 各包一层）。
	wantCode(t, AsError(AsError(host.ErrClosed)), wire.CodeClosed, "")
}

func TestV2CreateIdempotent(t *testing.T) {
	app := newApp(t)
	out, e := call2(t, app, "session.create", `{"client_id":"k1"}`)
	if e != nil {
		t.Fatal(e)
	}
	snap := out.(wire.SessionSnapshot)
	if snap.ID == "" || snap.RunState != wire.RunIdle || snap.Origin != "new" {
		t.Fatalf("快照：%+v", snap)
	}
	out2, e := call2(t, app, "session.create", `{"client_id":"k1"}`)
	if e != nil || out2.(wire.SessionSnapshot).ID != snap.ID {
		t.Fatalf("同一 client_id 重发应返回同一会话：%v %v", out2, e)
	}
	_, e = call2(t, app, "session.create", `{"session":"`+snap.ID+`"}`)
	wantCode(t, e, wire.CodeConflict, wire.ConflictExists)
	_, e = call2(t, app, "session.create", `{"session":"a/b"}`)
	wantCode(t, e, wire.CodeInvalid, "")
}

func TestV2SendRewind(t *testing.T) {
	app := newApp(t)
	ctx := context.Background()
	if _, e := call2(t, app, "session.create", `{"session":"s"}`); e != nil {
		t.Fatal(e)
	}
	_, e := call2(t, app, "session.send", `{"session":"s","text":"a"}`)
	wantCode(t, e, wire.CodeBadRequest, "")
	for i, s := range []string{"a", "b"} {
		out, e := call2(t, app, "session.send", fmt.Sprintf(`{"session":"s","text":%q,"client_id":"c%d"}`, s, i))
		if e != nil {
			t.Fatal(e)
		}
		if r := out.(wire.SendResult); r.ClientID != fmt.Sprintf("c%d", i) {
			t.Fatalf("send 结果：%+v", r)
		}
		hd, _ := app.Host().Get("s")
		if err := hd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	// 重发同一 client_id 不再开回合。
	if _, e := call2(t, app, "session.send", `{"session":"s","text":"a","client_id":"c0"}`); e != nil {
		t.Fatal(e)
	}
	hd, _ := app.Host().Get("s")
	_ = hd.Wait()
	out, _ := call2(t, app, "session.get", `{"session":"s"}`)
	snap := out.(wire.SessionSnapshot)
	if snap.Log.Len != 4 {
		t.Fatalf("重发同一 client_id 不应再开回合：len=%d", snap.Log.Len)
	}
	// 用户条目带 client_ids。
	er, e := call2(t, app, "session.entries", `{"session":"s"}`)
	if e != nil {
		t.Fatal(e)
	}
	ents := er.(wire.EntriesResult).Entries
	if len(ents) != 4 || ents[0].Item.Kind != wire.KindUser {
		t.Fatalf("entries：%+v", ents)
	}
	// 过期游标被拒，日志不变。
	_, e = call2(t, app, "session.rewind", fmt.Sprintf(`{"session":"s","to_turn":2,"if_cursor":%d}`, snap.Cursor-1))
	wantCode(t, e, wire.CodeConflict, wire.ConflictStale)
	rr, e := call2(t, app, "session.rewind", fmt.Sprintf(`{"session":"s","to_turn":2,"if_cursor":%d}`, snap.Cursor))
	if e != nil || rr.(wire.RewindResult).To != 2 {
		t.Fatalf("rewind：%v %v", rr, e)
	}
	// 绝对目标：重发同一 to_turn 无害。
	if rr, e := call2(t, app, "session.rewind", `{"session":"s","to_turn":2}`); e != nil || rr.(wire.RewindResult).To != 2 {
		t.Fatalf("重发 rewind：%v %v", rr, e)
	}
	// 空闲时 steer：没有进行中的回合 → invalid；cancel 幂等。
	_, e = call2(t, app, "session.steer", `{"session":"s","text":"x","client_id":"st"}`)
	wantCode(t, e, wire.CodeInvalid, "")
	if _, e := call2(t, app, "session.cancel", `{"session":"s"}`); e != nil {
		t.Fatal(e)
	}
	// settings：空闲时已落库。
	so, e := call2(t, app, "session.settings", `{"session":"s","max_steps":7}`)
	if e != nil || !so.(wire.SettingsResult).Committed || so.(wire.SettingsResult).Settings.MaxSteps != 7 {
		t.Fatalf("settings：%v %v", so, e)
	}
	if out, _ := call2(t, app, "session.get", `{"session":"s"}`); out.(wire.SessionSnapshot).Settings.MaxSteps != 7 {
		t.Fatal("快照应带已提交的设置")
	}
	_ = ctx
}

func TestV2HelloAndFork(t *testing.T) {
	app := newApp(t)
	_, e := call2(t, app, "hello", `{"client":"t","protocol":{"min":3,"max":4}}`)
	wantCode(t, e, wire.CodeUnsupported, "")
	out, e := call2(t, app, "hello", `{"client":"t","protocol":{"min":1,"max":2}}`)
	if e != nil || out.(wire.HelloResult).Protocol != 2 || out.(wire.HelloResult).Principal.Role != wire.RoleOperator {
		t.Fatalf("hello：%v %v", out, e)
	}
	if _, e := call2(t, app, "session.create", `{"session":"src"}`); e != nil {
		t.Fatal(e)
	}
	f1, e := call2(t, app, "session.fork", `{"session":"src","client_id":"f"}`)
	if e != nil {
		t.Fatal(e)
	}
	f2, e := call2(t, app, "session.fork", `{"session":"src","client_id":"f"}`)
	if e != nil || f2.(wire.SessionSnapshot).ID != f1.(wire.SessionSnapshot).ID {
		t.Fatalf("同一 client_id 的 fork 重发应返回同一会话：%v %v", f2, e)
	}
	if f1.(wire.SessionSnapshot).Parent != "src" || f1.(wire.SessionSnapshot).Origin != "fork" {
		t.Fatalf("fork 快照：%+v", f1)
	}
	_, e = call2(t, app, "session.fork", `{"session":"src","to":"`+f1.(wire.SessionSnapshot).ID+`"}`)
	wantCode(t, e, wire.CodeConflict, wire.ConflictExists)
}

func TestV2Artifacts(t *testing.T) {
	app := newApp(t)
	_, e := call2(t, app, "artifact.put", `{"session":"s","mime":"text/plain","base64":"eA=="}`)
	wantCode(t, e, wire.CodeUnsupported, "")
	app.o.Artifacts = &memArt{}
	if _, e := call2(t, app, "session.create", `{"session":"s"}`); e != nil {
		t.Fatal(e)
	}
	body := "hello 你好"
	out, e := call2(t, app, "artifact.put", `{"session":"s","mime":"text/plain","base64":"`+base64.StdEncoding.EncodeToString([]byte(body))+`"}`)
	if e != nil {
		t.Fatal(e)
	}
	ref := out.(wire.ArtifactPutResult).Ref
	g, e := call2(t, app, "artifact.get", `{"session":"s","ref":"`+ref+`"}`)
	if e != nil || g.(wire.ArtifactGetResult).Text != body || g.(wire.ArtifactGetResult).Size != len(body) {
		t.Fatalf("artifact.get：%+v %v", g, e)
	}
	part, e := call2(t, app, "artifact.get", `{"session":"s","ref":"`+ref+`","offset":6,"length":3}`)
	if e != nil || part.(wire.ArtifactGetResult).Text != "你" {
		t.Fatalf("分段读：%+v %v", part, e)
	}
	_, e = call2(t, app, "artifact.get", `{"session":"s","ref":"artifact://../../etc"}`)
	wantCode(t, e, wire.CodeInvalid, "")
	// 媒体部件必须引用已上传的工件。
	_, e = call2(t, app, "session.send", `{"session":"s","client_id":"m","content":[{"kind":"media","media":{"ref":"artifact://`+
		hex.EncodeToString(make([]byte, 32))+`","mime":"image/png"}}]}`)
	wantCode(t, e, wire.CodeInvalid, "")
	if _, e := call2(t, app, "session.send", `{"session":"s","client_id":"m2","content":[{"kind":"text","text":"看"},`+
		`{"kind":"media","media":{"ref":"`+ref+`","mime":"text/plain"}}]}`); e != nil {
		t.Fatal(e)
	}
}
