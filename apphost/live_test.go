package apphost

// v1 整体验收（计划/01 第三节第 2 条）里能自动跑的部分：真实模型、经应用接口层（protocol 2）驱动，
// 与桌面端 / 网页端走同一条路径。需要网络与 API key，缺省跳过：
//
//	TENON_LIVE=1 go test -run TestLive -v -timeout 30m ./apphost/   （在 framework/ 下；key 等读环境变量）
//
// 或者用 framework/scripts/accept_v1.sh（读 framework/.env，再跑"中途杀进程后重启续跑"）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"tenon/appapi"
	"tenon/runtime/agentdef"
)

type live struct {
	t     *testing.T
	root  string
	home  string // 用户级目录：会话库在这里
	built *Built
	rt    *Runtime
}

func startLive(t *testing.T) *live {
	t.Helper()
	if os.Getenv("TENON_LIVE") != "1" {
		t.Skip("真实模型验收：设 TENON_LIVE=1 才跑")
	}
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("TENON_HOME", home)
	extra := filepath.Join(t.TempDir(), "live-profile.toml")
	if err := os.WriteFile(extra, []byte("[[plugin]]\nname = \"subagent\"\nenabled = true\n[[plugin]]\nname = \"workgroup\"\nenabled = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("hello tenon\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt, err := Start(StartOptions{Flags: Flags{Workdir: root, Profile: extra, Yes: true, MaxSteps: -1,
		Base: os.Getenv("TENON_BASE_URL"), Model: os.Getenv("TENON_MODEL")},
		App: AppOptions{MaxSteps: -1, ModesWorkdir: root}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	built, err := rt.BuildApp()
	if err != nil {
		t.Fatal(err)
	}
	// 低权限智能体：只读档位上限，能委派子智能体（子会话叠加父的上限）。
	defs := agentdef.Mem()
	if err := defs.Put(agentdef.Ref{Namespace: agentdef.Platform, Name: "reader"}, agentdef.Files{agentdef.ManifestName: []byte(
		"format = \"tenon/v1\"\nid = \"reader\"\ndescription = \"只读的智能体\"\n" +
			"tools = [\"read_file\", \"glob\", \"write_file\", \"subagent\"]\n[use.permission]\nmode = \"read-only\"\n")}); err != nil {
		t.Fatal(err)
	}
	built.Builder.Definitions = agentdef.Multi(rt.Rt.Defs, defs)
	for _, e := range built.Ready(context.Background()) {
		t.Logf("ready: %v", e)
	}
	return &live{t: t, root: root, home: home, built: built, rt: rt}
}

func (l *live) call(cmd string, args any) json.RawMessage {
	l.t.Helper()
	b, _ := json.Marshal(args)
	res, err := l.built.API.Call(context.Background(), cmd, b)
	if err != nil {
		l.t.Fatalf("%s: %v", cmd, err)
	}
	out, _ := json.Marshal(res)
	return out
}

// text 会话全部条目的 JSON（断言用：只看里面有没有某段文字）。
func (l *live) text(session string) string {
	return string(l.call("session.entries", map[string]any{"session": session}))
}

// tryText 同 text，会话还不存在时返回空（成员会话由组长回合派生，开始时还没有）。
func (l *live) tryText(session string) string {
	b, _ := json.Marshal(map[string]any{"session": session})
	res, err := l.built.API.Call(context.Background(), "session.entries", b)
	if err != nil {
		return ""
	}
	out, _ := json.Marshal(res)
	return string(out)
}

func (l *live) runState(session string) string {
	var s struct {
		RunState string `json:"run_state"`
	}
	_ = json.Unmarshal(l.call("session.get", map[string]any{"session": session}), &s)
	return s.RunState
}

// ask 建会话（可指定定义）、发一条消息，等到 done(条目文本) 为真且会话空闲。
func (l *live) ask(session, definition, msg string, timeout time.Duration, done func(string) bool) string {
	l.t.Helper()
	l.call("session.create", map[string]any{"session": session, "definition": definition})
	l.call("session.send", map[string]any{"session": session, "text": msg, "client_id": fmt.Sprintf("c-%d", time.Now().UnixNano())})
	dl := time.Now().Add(timeout)
	for time.Now().Before(dl) {
		time.Sleep(time.Second)
		if txt := l.text(session); done(txt) && l.runState(session) == "idle" {
			return txt
		}
	}
	l.t.Fatalf("会话 %s 在 %v 内没完成；条目：%s", session, timeout, clipText(l.text(session), 3000))
	return ""
}

func (l *live) db() *sql.DB {
	l.t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(l.home, "state.db"))+"?mode=ro")
	if err != nil {
		l.t.Fatal(err)
	}
	l.t.Cleanup(func() { db.Close() })
	return db
}

func (l *live) children(parent string) []string {
	l.t.Helper()
	rows, err := l.db().Query(`SELECT id FROM sessions WHERE parent_id = ? ORDER BY id`, parent)
	if err != nil {
		l.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		out = append(out, id)
	}
	return out
}

func clipText(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func contains(sub string) func(string) bool {
	return func(s string) bool { return strings.Contains(s, sub) }
}

func TestLive(t *testing.T) {
	l := startLive(t)

	t.Run("1 单聊", func(t *testing.T) {
		l.t = t
		l.ask("chat", "", "1 加 1 等于几？只回答阿拉伯数字。", 3*time.Minute, contains(`"assistant"`))
	})

	t.Run("2 主会话调子智能体", func(t *testing.T) {
		l.t = t
		txt := l.ask("sub", "", "用 subagent 工具委派一个子任务：读工作区的 note.txt，统计字符数（不含换行）。"+
			"拿到子智能体的结果后告诉我这个数字。不要自己读文件。", 6*time.Minute, contains("11"))
		kids := l.children("sub")
		if len(kids) == 0 {
			t.Fatalf("应派生子会话；主会话条目：%s", clipText(txt, 2000))
		}
		t.Logf("子会话：%v", kids)
	})

	t.Run("3 工作组组长派活并收回结果", func(t *testing.T) {
		l.t = t
		member := "team/m-counter"
		l.ask("team", "workgroup-leader", "用 team_spawn 派一个名为 counter 的成员，任务：读工作区的 note.txt，统计字符数（不含换行），只回答数字。"+
			"派完就结束本轮；成员的结果送回来后，用一句话告诉我这个数字。不要自己读文件。", 8*time.Minute,
			func(s string) bool {
				return strings.Count(s, "11") >= 1 && strings.Contains(s, "counter") && strings.Contains(l.tryText(member), "11")
			})
		t.Logf("成员会话 %s 已完成", member)
	})

	t.Run("4 低权限智能体委派写文件被拦下", func(t *testing.T) {
		l.t = t
		txt := l.ask("guard", "reader", "用 subagent 工具委派子智能体：在工作区写文件 secret.txt，内容 pwned。"+
			"不要自己写；把子智能体的原话结果告诉我。", 6*time.Minute, contains(`"assistant"`))
		if _, err := os.Stat(filepath.Join(l.root, "secret.txt")); err == nil {
			t.Fatal("只读上限下的子智能体不应写出文件")
		}
		// 只读上限叠加到子会话：写类工具取值为 deny，根本不进子会话的工具列表（03 约束清单"取值 deny 的工具"），
		// 所以子智能体没法调用写工具；万一调用了也会被权限拒绝。两种都算拦下，只要文件没写出来、没有成功的写调用。
		kids := l.children("guard")
		if len(kids) == 0 {
			t.Fatalf("应派生子会话；主会话：%s", clipText(txt, 2000))
		}
		for _, k := range kids {
			kt := l.text(k)
			for _, w := range []string{`"name":"write_file"`, `"name":"shell"`, `"name":"edit"`} {
				if strings.Contains(kt, w) && !strings.Contains(kt, "权限拒绝") {
					t.Fatalf("子会话 %s 调了 %s 且没被拒绝：%s", k, w, clipText(kt, 3000))
				}
			}
			t.Logf("子会话 %s 条目：%s", k, clipText(kt, 1500))
		}
		// explain trace：本会话涉及的 trace 逐个渲染，能看到经过的会话与工具。
		rows, err := l.db().Query(`SELECT DISTINCT trace FROM trace_events WHERE session = 'guard' OR session LIKE 'guard/%'`)
		if err != nil {
			t.Fatal(err)
		}
		var traces []string
		for rows.Next() {
			var tr string
			_ = rows.Scan(&tr)
			traces = append(traces, tr)
		}
		rows.Close()
		for _, tr := range traces {
			out, err := appapi.RenderTrace(context.Background(), l.built.DB, tr)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("explain trace:\n%s", out)
		}
		if len(traces) == 0 {
			t.Log("没有 trace 记录")
		}
	})
}
