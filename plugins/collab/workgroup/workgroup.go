// Package workgroup 工作组（M6.8）：领导会话 + 成员会话 + 共享看板。
//
// 成员是普通会话（origin=member，parent_id=领导会话 id，id=<领导>/m-<名字>），持久、可单独打开。
// 领导用 team_spawn 异步派活：成员回合在后台跑，结束时把最终文本作为一条输入投递回领导 inbox
// （领导空闲则立即开回合，忙则排队）。team_send 双向发消息（同样走 inbox，item id 幂等）。
// 看板存插件自有表 p_workgroup_tasks，按领导会话 id 分组；领取 / 完成带修订号做条件写。
package workgroup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"ybase-kernel/core/host"
	"ybase-kernel/core/session"
	"ybase-kernel/core/store/sqlite"
	"ybase-kernel/core/toolhub"
)

// MemberTimeout 单个成员回合的上限。
const MemberTimeout = 30 * time.Minute

// MaxMembers 每个工作组的成员上限。
const MaxMembers = 8

var nameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

var migrations = []sqlite.Migration{{Version: 1, Stmts: []string{
	`CREATE TABLE p_workgroup_tasks(
		team TEXT NOT NULL, id INTEGER NOT NULL,
		subject TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
		owner TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'pending',
		result TEXT NOT NULL DEFAULT '', rev INTEGER NOT NULL DEFAULT 1,
		created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
		PRIMARY KEY(team, id))`,
}}, {Version: 2, Stmts: []string{
	// 成员回合的投递账：开始时记 running，结束时记 done + 结果；投递成功后删除。
	// 重启后 Recover 补投（item id 固定，inbox 准入幂等，不会重复）。
	`CREATE TABLE p_workgroup_runs(
		member TEXT NOT NULL PRIMARY KEY, leader TEXT NOT NULL, name TEXT NOT NULL,
		item_id TEXT NOT NULL, state TEXT NOT NULL, msg TEXT NOT NULL DEFAULT '',
		updated_at INTEGER NOT NULL)`,
}}}

// Group 进程内工作组服务。
type Group struct {
	Host   *host.Host
	DB     *sqlite.DB
	Member host.Factory // 成员会话工厂（成员工具集，不含 team_spawn）

	mu      sync.Mutex
	running map[string]bool // 成员 id → 回合进行中
	wg      sync.WaitGroup
	ctx     context.Context
	cancel  context.CancelFunc
}

// New 建表并返回服务。
func New(ctx context.Context, h *host.Host, db *sqlite.DB, member host.Factory) (*Group, error) {
	if err := db.Migrate(ctx, "workgroup", migrations); err != nil {
		return nil, err
	}
	c, cancel := context.WithCancel(context.Background())
	return &Group{Host: h, DB: db, Member: member, running: map[string]bool{}, ctx: c, cancel: cancel}, nil
}

// Close 取消所有后台成员回合并等待退出。
func (g *Group) Close() {
	g.cancel()
	g.wg.Wait()
}

// Wait 等所有后台成员回合结束（测试用）。
func (g *Group) Wait() { g.wg.Wait() }

// MemberID 成员会话 id。
func MemberID(leader, name string) string { return leader + "/m-" + name }

// team 由会话 id 求工作组（领导会话 id）与自身名字（领导为 "lead"）。
func team(sid string) (leader, self string) {
	if i := strings.LastIndex(sid, "/m-"); i >= 0 {
		return sid[:i], sid[i+3:]
	}
	return sid, "lead"
}

func obj(props string, req ...string) json.RawMessage {
	r, _ := json.Marshal(req)
	return json.RawMessage(`{"type":"object","properties":{` + props + `},"required":` + string(r) + `}`)
}

// LeaderTools 领导会话的工具：team_spawn、team_send、看板。
func (g *Group) LeaderTools() []*toolhub.Tool {
	return append([]*toolhub.Tool{g.spawnTool(), g.sendTool()}, g.boardTools()...)
}

// MemberTools 成员会话的工具：team_send（发给领导或其他成员）、看板。
func (g *Group) MemberTools() []*toolhub.Tool {
	return append([]*toolhub.Tool{g.sendTool()}, g.boardTools()...)
}

func (g *Group) spawnTool() *toolhub.Tool {
	no := false
	return &toolhub.Tool{
		Spec: session.ToolSpec{Name: "team_spawn",
			Description: "派一个工作组成员（独立会话、同样的基础工具 + 看板）去做自包含的任务，立即返回，不等结果。" +
				"成员做完时它的最终回答会作为一条新消息送回给你。同名成员已存在则把 task 作为下一条消息发给它。",
			Schema: obj(`"name":{"type":"string","description":"成员名，小写字母数字和连字符"},"task":{"type":"string","description":"自包含的任务说明"}`, "name", "task")},
		Risk: toolhub.Write, Parallel: &no, Attrs: map[string]any{toolhub.AttrEffect: toolhub.EffectSpawn},
		Items: []string{ItemSpawn},
		Call: func(ctx context.Context, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
			var a struct{ Name, Task string }
			if err := json.Unmarshal(raw, &a); err != nil || strings.TrimSpace(a.Task) == "" || !nameRe.MatchString(a.Name) {
				return toolhub.Errorf("需要合法的 name（小写字母数字和连字符）与非空 task"), nil
			}
			leader, self := team(cx.SessionID)
			if self != "lead" {
				return toolhub.Errorf("只有领导会话能派成员"), nil
			}
			id := MemberID(leader, a.Name)
			cx.ChildSessionID = id
			if err := g.start(leader, id, a.Name, a.Task); err != nil {
				return toolhub.Errorf("%v", err), nil
			}
			return toolhub.Text(fmt.Sprintf("已派成员 %s（会话 %s），完成后结果会作为新消息送达。", a.Name, id)), nil
		},
	}
}

// start 后台跑成员一个回合；结束后把结果投递回领导。
func (g *Group) start(leader, id, name, task string) error {
	g.mu.Lock()
	if g.running[id] {
		g.mu.Unlock()
		return fmt.Errorf("成员 %s 正在工作；用 team_send 给它发消息", name)
	}
	n := 0
	for k := range g.running {
		if strings.HasPrefix(k, leader+"/m-") {
			n++
		}
	}
	if n >= MaxMembers {
		g.mu.Unlock()
		return fmt.Errorf("同时工作的成员已达上限 %d", MaxMembers)
	}
	g.running[id] = true
	g.mu.Unlock()
	item := host.NewItemID()
	if _, err := g.DB.SQL().Exec(`INSERT INTO p_workgroup_runs(member, leader, name, item_id, state, updated_at)
		VALUES(?, ?, ?, ?, 'running', ?) ON CONFLICT(member) DO UPDATE SET leader = excluded.leader, name = excluded.name,
		item_id = excluded.item_id, state = 'running', msg = '', updated_at = excluded.updated_at`,
		id, leader, name, item, time.Now().UnixMilli()); err != nil {
		g.mu.Lock()
		delete(g.running, id)
		g.mu.Unlock()
		return err
	}
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		ctx, cancel := context.WithTimeout(g.ctx, MemberTimeout)
		defer cancel()
		out, err := g.Host.SpawnAs(ctx, leader, id, "member", g.Member, task)
		g.mu.Lock()
		delete(g.running, id)
		g.mu.Unlock()
		if g.ctx.Err() != nil {
			return // 进程退出：账上仍是 running，重启后 Recover 告知领导该成员被中断
		}
		msg := fmt.Sprintf("[成员 %s 完成]\n%s", name, out)
		if err != nil {
			msg = fmt.Sprintf("[成员 %s 失败] %v", name, err)
		}
		bg := context.Background()
		if _, err := g.DB.SQL().ExecContext(bg, `UPDATE p_workgroup_runs SET state = 'done', msg = ?, updated_at = ?
			WHERE member = ? AND item_id = ?`, msg, time.Now().UnixMilli(), id, item); err != nil {
			return
		}
		g.settle(bg, id, leader, name, item, msg)
	}()
	return nil
}

// settle 投递一条结果并销账；投递失败则留账，下次 Recover 再投。
func (g *Group) settle(ctx context.Context, member, leader, name, item, msg string) error {
	if err := g.deliverID(ctx, leader, "member:"+name, item, msg); err != nil {
		return err
	}
	_, err := g.DB.SQL().ExecContext(ctx, `DELETE FROM p_workgroup_runs WHERE member = ? AND item_id = ?`, member, item)
	return err
}

// Recover 补投上次进程未投递的成员结果：已完成的投结果；进程退出时仍在跑的，告知领导它被中断。
// 启动时调用一次（在开始派新成员之前）。返回补投条数。
func (g *Group) Recover(ctx context.Context) (int, error) {
	rows, err := g.DB.SQL().QueryContext(ctx, `SELECT member, leader, name, item_id, state, msg FROM p_workgroup_runs ORDER BY updated_at`)
	if err != nil {
		return 0, err
	}
	type run struct{ member, leader, name, item, state, msg string }
	var rs []run
	for rows.Next() {
		var r run
		if err := rows.Scan(&r.member, &r.leader, &r.name, &r.item, &r.state, &r.msg); err != nil {
			rows.Close()
			return 0, err
		}
		rs = append(rs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rs {
		g.mu.Lock()
		busy := g.running[r.member]
		g.mu.Unlock()
		if busy {
			continue
		}
		msg := r.msg
		if r.state != "done" {
			msg = fmt.Sprintf("[成员 %s 中断] 上次进程退出时它还没做完；会话已保留，可用 team_send 让它继续。", r.name)
		}
		if err := g.settle(ctx, r.member, r.leader, r.name, r.item, msg); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// deliver 把一条消息作为输入投递到会话 inbox（会话未打开则先打开）。
func (g *Group) deliver(ctx context.Context, to, from, text string) error {
	return g.deliverID(ctx, to, from, host.NewItemID(), text)
}

// deliverID 同 deliver，item id 由调用方给（重投幂等）。
func (g *Group) deliverID(ctx context.Context, to, from, item, text string) error {
	hd, ok := g.Host.Get(to)
	if !ok {
		var err error
		if hd, err = g.Host.Open(ctx, to); err != nil {
			return err
		}
	}
	return hd.Do(host.Command{Kind: host.Send, ItemID: item, Text: text, Source: from})
}

func (g *Group) sendTool() *toolhub.Tool {
	return &toolhub.Tool{
		Spec: session.ToolSpec{Name: "team_send",
			Description: "给工作组里的另一位发消息：to 为 \"lead\"（领导）或成员名。消息进入对方 inbox，对方空闲时立即处理。",
			Schema:      obj(`"to":{"type":"string"},"message":{"type":"string"}`, "to", "message")},
		Risk: toolhub.Write, Attrs: map[string]any{toolhub.AttrEffect: toolhub.EffectCollab},
		Items: []string{ItemSend},
		Call: func(ctx context.Context, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
			var a struct{ To, Message string }
			if err := json.Unmarshal(raw, &a); err != nil || strings.TrimSpace(a.Message) == "" {
				return toolhub.Errorf("需要 to 与非空 message"), nil
			}
			leader, self := team(cx.SessionID)
			if a.To == self {
				return toolhub.Errorf("不能发给自己"), nil
			}
			text := fmt.Sprintf("[来自 %s] %s", self, a.Message)
			if a.To == "lead" {
				if err := g.deliver(ctx, leader, "member:"+self, text); err != nil {
					return toolhub.Errorf("发送失败：%v", err), nil
				}
				return toolhub.Text("已发送给 lead"), nil
			}
			if !nameRe.MatchString(a.To) {
				return toolhub.Errorf("成员名不合法：%s", a.To), nil
			}
			id := MemberID(leader, a.To)
			ok, err := g.DB.Exists(ctx, id)
			if err != nil {
				return toolhub.Output{}, err
			}
			if !ok {
				return toolhub.Errorf("没有成员 %s（先用 team_spawn 派出）", a.To), nil
			}
			g.mu.Lock()
			busy := g.running[id]
			g.mu.Unlock()
			if busy { // 回合进行中：进 inbox，当前回合结束后处理（结果随后台回合一并回报）
				hd, ok := g.Host.Get(id)
				if ok {
					if err := hd.Do(host.Command{Kind: host.Send, ItemID: host.NewItemID(), Text: text, Source: "member:" + self}); err != nil {
						return toolhub.Errorf("发送失败：%v", err), nil
					}
					return toolhub.Text("已发送给 " + a.To + "（它正在工作，处理完当前任务后会看到）"), nil
				}
			}
			if err := g.start(leader, id, a.To, text); err != nil { // 空闲成员：开新回合，完成后结果回报领导
				return toolhub.Errorf("%v", err), nil
			}
			return toolhub.Text("已发送给 " + a.To), nil
		},
	}
}

// Task 看板任务。
type Task struct {
	ID          int    `json:"id"`
	Subject     string `json:"subject"`
	Description string `json:"description,omitempty"`
	Owner       string `json:"owner,omitempty"`
	Status      string `json:"status"`
	Result      string `json:"result,omitempty"`
	Rev         int    `json:"rev"`
}

// ErrConflict 条件写失败（修订号不符或状态不允许）。
var ErrConflict = errors.New("workgroup: 任务已被改动")

// Create 新建任务。
func (g *Group) Create(ctx context.Context, team, subject, desc string) (Task, error) {
	now := time.Now().UnixMilli()
	var id int
	err := g.DB.SQL().QueryRowContext(ctx, `INSERT INTO p_workgroup_tasks(team, id, subject, description, created_at, updated_at)
		VALUES(?, (SELECT COALESCE(MAX(id),0)+1 FROM p_workgroup_tasks WHERE team = ?), ?, ?, ?, ?) RETURNING id`,
		team, team, subject, desc, now, now).Scan(&id)
	if err != nil {
		return Task{}, err
	}
	return Task{ID: id, Subject: subject, Description: desc, Status: "pending", Rev: 1}, nil
}

// List 列出任务。
func (g *Group) List(ctx context.Context, team string) ([]Task, error) {
	rows, err := g.DB.SQL().QueryContext(ctx, `SELECT id, subject, description, owner, status, result, rev
		FROM p_workgroup_tasks WHERE team = ? ORDER BY id`, team)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.Subject, &t.Description, &t.Owner, &t.Status, &t.Result, &t.Rev); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (g *Group) get(ctx context.Context, team string, id int) (Task, error) {
	var t Task
	err := g.DB.SQL().QueryRowContext(ctx, `SELECT id, subject, description, owner, status, result, rev
		FROM p_workgroup_tasks WHERE team = ? AND id = ?`, team, id).
		Scan(&t.ID, &t.Subject, &t.Description, &t.Owner, &t.Status, &t.Result, &t.Rev)
	if err == sql.ErrNoRows {
		return t, fmt.Errorf("没有任务 #%d", id)
	}
	return t, err
}

// Claim 领取 pending 任务（或自己已领的任务）。
func (g *Group) Claim(ctx context.Context, team string, id int, who string) (Task, error) {
	t, err := g.get(ctx, team, id)
	if err != nil {
		return t, err
	}
	if t.Status == "completed" || (t.Owner != "" && t.Owner != who) {
		return t, fmt.Errorf("%w：#%d 状态 %s，负责人 %s", ErrConflict, id, t.Status, t.Owner)
	}
	return g.update(ctx, team, t, who, "in_progress", "")
}

// Complete 完成任务（只能由负责人或领导完成）。
func (g *Group) Complete(ctx context.Context, team string, id int, who, result string) (Task, error) {
	t, err := g.get(ctx, team, id)
	if err != nil {
		return t, err
	}
	if t.Status == "completed" {
		return t, fmt.Errorf("%w：#%d 已完成", ErrConflict, id)
	}
	if who != "lead" && t.Owner != who {
		return t, fmt.Errorf("%w：#%d 负责人是 %q，先 task_claim", ErrConflict, id, t.Owner)
	}
	owner := t.Owner
	if owner == "" {
		owner = who
	}
	return g.update(ctx, team, t, owner, "completed", result)
}

func (g *Group) update(ctx context.Context, team string, t Task, owner, status, result string) (Task, error) {
	res, err := g.DB.SQL().ExecContext(ctx, `UPDATE p_workgroup_tasks SET owner = ?, status = ?, result = ?, rev = rev + 1, updated_at = ?
		WHERE team = ? AND id = ? AND rev = ?`, owner, status, result, time.Now().UnixMilli(), team, t.ID, t.Rev)
	if err != nil {
		return t, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return t, fmt.Errorf("%w：#%d", ErrConflict, t.ID)
	}
	t.Owner, t.Status, t.Result, t.Rev = owner, status, result, t.Rev+1
	return t, nil
}

func (g *Group) boardTools() []*toolhub.Tool {
	js := func(v any) toolhub.Output {
		b, _ := json.Marshal(v)
		return toolhub.Text(string(b))
	}
	wrap := func(t Task, err error) (toolhub.Output, error) {
		if err != nil {
			if errors.Is(err, ErrConflict) || strings.HasPrefix(err.Error(), "没有任务") {
				return toolhub.Errorf("%v", err), nil
			}
			return toolhub.Output{}, err
		}
		return js(t), nil
	}
	no := false
	return []*toolhub.Tool{
		{Spec: session.ToolSpec{Name: "task_create", Description: "在工作组看板上新建任务（pending、无负责人）。",
			Schema: obj(`"subject":{"type":"string"},"description":{"type":"string"}`, "subject")},
			Risk: toolhub.Write, Parallel: &no, Attrs: map[string]any{toolhub.AttrEffect: toolhub.EffectCollab},
			Items: []string{ItemBoard},
			Call: func(ctx context.Context, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
				var a struct{ Subject, Description string }
				if err := json.Unmarshal(raw, &a); err != nil || strings.TrimSpace(a.Subject) == "" {
					return toolhub.Errorf("subject 不能为空"), nil
				}
				leader, _ := team(cx.SessionID)
				return wrap(g.Create(ctx, leader, a.Subject, a.Description))
			}},
		{Spec: session.ToolSpec{Name: "task_list", Description: "列出工作组看板上的全部任务（id、状态、负责人、结果）。",
			Schema: json.RawMessage(`{"type":"object","properties":{}}`)},
			Risk: toolhub.Read, Items: []string{ItemView},
			Call: func(ctx context.Context, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
				leader, _ := team(cx.SessionID)
				ts, err := g.List(ctx, leader)
				if err != nil {
					return toolhub.Output{}, err
				}
				if ts == nil {
					ts = []Task{}
				}
				return js(ts), nil
			}},
		{Spec: session.ToolSpec{Name: "task_claim", Description: "领取一个 pending 任务（成为负责人，状态改为 in_progress）。",
			Schema: obj(`"id":{"type":"integer"}`, "id")},
			Risk: toolhub.Write, Parallel: &no, Attrs: map[string]any{toolhub.AttrEffect: toolhub.EffectCollab},
			Items: []string{ItemBoard},
			Call: func(ctx context.Context, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
				var a struct{ ID int }
				if err := json.Unmarshal(raw, &a); err != nil {
					return toolhub.Errorf("需要 id"), nil
				}
				leader, self := team(cx.SessionID)
				return wrap(g.Claim(ctx, leader, a.ID, self))
			}},
		{Spec: session.ToolSpec{Name: "task_complete", Description: "把自己负责的任务标为完成，附一句结果。",
			Schema: obj(`"id":{"type":"integer"},"result":{"type":"string"}`, "id")},
			Risk: toolhub.Write, Parallel: &no, Attrs: map[string]any{toolhub.AttrEffect: toolhub.EffectCollab},
			Items: []string{ItemBoard},
			Call: func(ctx context.Context, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
				var a struct {
					ID     int
					Result string
				}
				if err := json.Unmarshal(raw, &a); err != nil {
					return toolhub.Errorf("需要 id"), nil
				}
				leader, self := team(cx.SessionID)
				return wrap(g.Complete(ctx, leader, a.ID, self, a.Result))
			}},
	}
}
