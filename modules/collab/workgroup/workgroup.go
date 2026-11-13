// Package workgroup 工作组（M6.8）：领导会话 + 成员会话 + 共享看板。
//
// 成员是普通会话（origin=member，parent_id=领导会话 id，id=<领导>/m-<名字>），持久、可单独打开。
// 领导用 team_spawn 异步派活：成员回合在后台跑，结束时把最终文本作为一条输入投递回领导 inbox
// （领导空闲则立即开回合，忙则排队）。team_send 双向发消息（同样走 inbox，item id 幂等）。
// 看板存集合 workgroup.tasks（T4a），按领导会话 id 分区；领取 / 完成用文档版本做条件写。
// 成员回合的投递账存集合 workgroup.runs（按成员分区）。两个集合都不碰 SQL，
// 所以任何实现了 store.CollectionStore 的存储都能装上工作组。
// v0.2.x 已发布的库里投递账还在 SQL 表 p_workgroup_runs：只有存储提供 store.SQL 时才搬，
// 搬完删表；不提供就跳过（内存存储没有这张表）。
package workgroup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// MemberTimeout 单个成员回合的上限。
const MemberTimeout = 30 * time.Minute

// MaxMembers 每个工作组的成员上限。
const MaxMembers = 8

var nameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// legacyMigrations 只给旧库用：v0.1.x / v0.2.x 的 schema_versions 里记着 workgroup 的 1 和 2，
// 版本号不能改，改了已有库的后续迁移会被跳过。表本身不再新建——任务板与投递账都已改集合，
// 旧行由 importTasks / importRuns 搬完删除。没有 SQL 出口的存储不跑这段。
var legacyMigrations = []store.Migration{{Version: 1, Stmts: []string{}}, {Version: 2, Stmts: []string{}}}

// runsPart 投递账的固定分区。账要全量补投，集合又没有"列出全部分区"，
// 所以不按成员分区，成员 id 放进 key；state 是索引，Recover 按它分段查。
const runsPart = "runs"

// runsSpec 投递账集合的结构。
var runsSpec = store.CollectionSpec{Version: 1, Indexes: []string{"state"}, Partition: "part"}

// Group 进程内工作组服务。
type Group struct {
	Host     *host.Host
	SQL      store.SQL          // 可选：只用于把 v0.2.x 的旧表搬进集合。没有 SQL 出口的存储为 nil
	Tasks    store.Collection   // 任务板（集合 workgroup.tasks，Assembly.Start 前由框架打开）
	Runs     store.Collection   // 投递账（集合 workgroup.runs，固定分区）
	Sessions store.SessionStore // 会话存在性检查
	Member   host.Factory       // 成员会话工厂（成员工具集，不含 team_spawn）

	mu      sync.Mutex
	running map[string]bool // 成员 id → 回合进行中
	wg      sync.WaitGroup
	ctx     context.Context
	cancel  context.CancelFunc
}

// New 返回服务。tasks、runs 是已打开的集合（Assembly.Start 前由框架打开）。
// sq 只给旧库搬家用，可以为 nil：没有 SQL 出口的存储没有旧表可搬。
func New(ctx context.Context, h *host.Host, sq store.SQL, tasks, runs store.Collection, sessions store.SessionStore, member host.Factory) (*Group, error) {
	if sq != nil {
		// 版本号占位，不建表：已发布库的 schema_versions 记着 1 和 2，跳过会让以后的迁移被漏掉。
		if err := sq.Migrate(ctx, "workgroup", legacyMigrations); err != nil {
			return nil, err
		}
	}
	g := &Group{Host: h, SQL: sq, Tasks: tasks, Runs: runs, Sessions: sessions, Member: member, running: map[string]bool{}}
	if err := g.importTasks(ctx); err != nil {
		return nil, err
	}
	if err := g.importRuns(ctx); err != nil {
		return nil, err
	}
	g.ctx, g.cancel = context.WithCancel(context.Background())
	return g, nil
}

// importTasks 把旧表 p_workgroup_tasks 搬进集合（T4a）。
//
// v0.1.x 已发布，别人的库里可能有任务板，升级后要能直接用。表不存在就跳过（内存存储没有这张表，
// 新库的迁移也不再建它）。失败返回错误且不删表，下次启动重跑；已搬过的行 Put 会冲突，按"已存在"跳过。
// 计数器取已搬任务的最大 id，否则下一次 Create 会与旧 id 撞车。
func (g *Group) importTasks(ctx context.Context) error {
	if g.SQL == nil {
		return nil // 没有 SQL 出口就没有旧表
	}
	rows, err := g.readOldTasks(ctx)
	if err != nil || rows == nil {
		return err
	}
	maxID := map[string]int{}
	for _, r := range rows {
		t := Task{ID: r.id, Subject: r.subject, Description: r.desc, Owner: r.owner, Status: r.status, Result: r.result}
		if err := g.importTask(ctx, r.team, t, r.rev); err != nil {
			return fmt.Errorf("workgroup: 搬任务 %s #%d：%w", r.team, r.id, err)
		}
		if r.id > maxID[r.team] {
			maxID[r.team] = r.id
		}
	}
	for team, n := range maxID {
		if err := g.importSeq(ctx, team, n); err != nil {
			return err
		}
	}
	if _, err := g.SQL.DB().ExecContext(ctx, `DROP TABLE p_workgroup_tasks`); err != nil {
		return fmt.Errorf("workgroup: 删除旧任务板：%w", err)
	}
	return nil
}

// run 一条投递账：成员回合开始时记 running，结束时记 done + 结果，投递成功后删除。
type run struct {
	member, leader, name, item, state, msg string
	updated                                int64
}

func runBody(r run) map[string]any {
	return map[string]any{
		"part": runsPart, "member": r.member, "leader": r.leader, "name": r.name, "item": r.item,
		"state": r.state, "msg": r.msg, "updated": r.updated,
	}
}

func runFromDoc(d store.Doc) run {
	return run{
		member: strOf(d.Body["member"]), leader: strOf(d.Body["leader"]), name: strOf(d.Body["name"]),
		item: strOf(d.Body["item"]), state: strOf(d.Body["state"]), msg: strOf(d.Body["msg"]),
		updated: numOf(d.Body["updated"]),
	}
}

// putRun 写入一条账。同成员已有账则覆盖（版本条件写，冲突重试）。
func (g *Group) putRun(ctx context.Context, r run) error {
	doc := store.Doc{Key: r.member, Body: runBody(r)}
	ver := int64(0)
	for {
		have, err := g.Runs.Get(ctx, runsPart, r.member)
		switch {
		case errors.Is(err, store.ErrNotFound):
			ver = 0
		case err != nil:
			return err
		default:
			ver = have.Version
		}
		if err := g.Runs.Put(ctx, runsPart, doc, ver); err != nil {
			if errors.Is(err, store.ErrConflict) {
				continue
			}
			return err
		}
		return nil
	}
}

// deleteRun 销账。账已被别人改过（版本不符）就留下，下次 Recover 再投。
func (g *Group) deleteRun(ctx context.Context, member, item string) error {
	have, err := g.Runs.Get(ctx, runsPart, member)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if runFromDoc(have).item != item {
		return nil // 这条账已经是下一次派活的了，不能删
	}
	err = g.Runs.Delete(ctx, runsPart, member, have.Version)
	if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}

// listRuns 读出全部未投递的账，按更新时间升序（补投顺序与写入顺序一致）。
func (g *Group) listRuns(ctx context.Context) ([]run, error) {
	var out []run
	for _, st := range []string{"running", "done"} {
		page := store.Page{}
		for {
			docs, next, err := g.Runs.Query(ctx, runsPart, "state", st, page)
			if err != nil {
				return nil, err
			}
			for _, d := range docs {
				out = append(out, runFromDoc(d))
			}
			if next.After == "" {
				break
			}
			page = next
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].updated < out[j].updated })
	return out, nil
}

// hasRun 成员是否还有未销的账（测试用）。
func (g *Group) hasRun(ctx context.Context, member string) bool {
	_, err := g.Runs.Get(ctx, runsPart, member)
	return err == nil
}

// importRuns 把 v0.2.x 的旧投递账 p_workgroup_runs 搬进集合 workgroup.runs。
//
// 规则与 importTasks 相同：没有 SQL 出口或表不存在就跳过；读完关掉再写（写连接只有一条）；
// 失败返回错误且不删表，下次启动重跑；集合里已有同成员的账按"已搬过"跳过，不覆盖。
func (g *Group) importRuns(ctx context.Context) error {
	if g.SQL == nil {
		return nil
	}
	rs, err := g.readOldRuns(ctx)
	if err != nil || rs == nil {
		return err
	}
	for _, r := range rs {
		doc := store.Doc{Key: r.member, Body: runBody(r)}
		if err := g.Runs.Put(ctx, runsPart, doc, 0); err != nil && !errors.Is(err, store.ErrConflict) {
			return fmt.Errorf("workgroup: 搬投递账 %s：%w", r.member, err)
		}
	}
	if _, err := g.SQL.DB().ExecContext(ctx, `DROP TABLE p_workgroup_runs`); err != nil {
		return fmt.Errorf("workgroup: 删除旧投递账：%w", err)
	}
	return nil
}

// readOldRuns 读出旧投递账的全部行。表不存在返回 nil。
func (g *Group) readOldRuns(ctx context.Context) ([]run, error) {
	rows, err := g.SQL.DB().QueryContext(ctx, `SELECT member, leader, name, item_id, state, msg, updated_at
		FROM p_workgroup_runs`)
	if err != nil {
		if isNoSuchTable(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("workgroup: 读旧投递账：%w", err)
	}
	defer rows.Close()
	out := []run{}
	for rows.Next() {
		var r run
		if err := rows.Scan(&r.member, &r.leader, &r.name, &r.item, &r.state, &r.msg, &r.updated); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// oldTask 旧表 p_workgroup_tasks 的一行。
type oldTask struct {
	team, subject, desc, owner, status, result string
	id, rev                                    int
}

// readOldTasks 读出旧任务板的全部行。表不存在返回 nil。
// 写连接只有一条，必须读完关掉再写，否则后面的 Put 会和这次查询抢同一条连接。
func (g *Group) readOldTasks(ctx context.Context) ([]oldTask, error) {
	rows, err := g.SQL.DB().QueryContext(ctx, `SELECT team, id, subject, description, owner, status, result, rev
		FROM p_workgroup_tasks`)
	if err != nil {
		if isNoSuchTable(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("workgroup: 读旧任务板：%w", err)
	}
	defer rows.Close()
	var out []oldTask
	for rows.Next() {
		var r oldTask
		if err := rows.Scan(&r.team, &r.id, &r.subject, &r.desc, &r.owner, &r.status, &r.result, &r.rev); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// importTask 搬一行。已搬过的行（版本已到旧 rev）按"已存在"跳过；搬到一半的补上修订号。
// 旧表的 rev 是列，集合版本由存储从 1 起、每次写入加一，所以要补写到原值，读回时 Rev 才保持原值。
func (g *Group) importTask(ctx context.Context, team string, t Task, rev int) error {
	doc := store.Doc{Key: taskKey(t.ID), Body: taskBody(team, t)}
	if err := g.Tasks.Put(ctx, team, doc, 0); err != nil && !errors.Is(err, store.ErrConflict) {
		return err
	}
	for {
		have, err := g.Tasks.Get(ctx, team, doc.Key)
		if err != nil {
			return err
		}
		if have.Version >= int64(rev) {
			return nil
		}
		if err := g.Tasks.Put(ctx, team, doc, have.Version); err != nil {
			if errors.Is(err, store.ErrConflict) {
				continue
			}
			return err
		}
	}
}

// importSeq 把分区计数器至少抬到已搬任务的最大 id。重跑时取较大值，不回退。
func (g *Group) importSeq(ctx context.Context, team string, n int) error {
	for {
		d, err := g.Tasks.Get(ctx, team, seqKey)
		if errors.Is(err, store.ErrNotFound) {
			d = store.Doc{Key: seqKey, Body: map[string]any{"team": team, seqMark: true, "n": int64(0), "status": "", "owner": ""}}
		} else if err != nil {
			return err
		}
		if numOf(d.Body["n"]) >= int64(n) {
			return nil
		}
		d.Body["n"] = int64(n)
		if err := g.Tasks.Put(ctx, team, d, d.Version); err != nil {
			if errors.Is(err, store.ErrConflict) {
				continue
			}
			return err
		}
		return nil
	}
}

// isNoSuchTable 表不存在。SQLite 报 "no such table"；其他方言的存储没有这张表时同样跳过。
func isNoSuchTable(err error) bool {
	return strings.Contains(err.Error(), "no such table")
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
	return append([]*toolhub.Tool{g.spawnTool(), g.sendTool(), g.stopTool()}, g.boardTools()...)
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
		Risk: toolhub.Write, Parallel: &no,
		Items: []string{ItemSpawn},
		Call: func(ctx context.Context, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
			var a struct{ Name, Task string }
			if err := json.Unmarshal(raw, &a); err != nil || strings.TrimSpace(a.Task) == "" || !nameRe.MatchString(a.Name) {
				return toolhub.Failf("需要合法的 name（小写字母数字和连字符）与非空 task"), nil
			}
			leader, self := team(cx.SessionID)
			if self != "lead" {
				return toolhub.Failf("只有领导会话能派成员"), nil
			}
			id := MemberID(leader, a.Name)
			cx.ChildSessionID = id
			if err := g.start(leader, id, a.Name, a.Task, cx.Ctx.Untrusted()); err != nil {
				return toolhub.Failf("%v", err), nil
			}
			return toolhub.Text(fmt.Sprintf("已派成员 %s（会话 %s），完成后结果会作为新消息送达。", a.Name, id)), nil
		},
	}
}

// start 后台跑成员一个回合；结束后把结果投递回领导。
// untrusted 派出时本回合的可信度：不可信回合派出的任务按不可信投给成员（审查 P1-3）。
func (g *Group) start(leader, id, name, task string, untrusted bool) error {
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
	if err := g.putRun(context.Background(), run{
		member: id, leader: leader, name: name, item: item, state: "running", updated: time.Now().UnixMilli(),
	}); err != nil {
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
		out, tainted, err := g.Host.SpawnAs(ctx, leader, id, "member", MemberDefinition, g.Member, task, untrusted)
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
		if err := g.putRun(bg, run{
			member: id, leader: leader, name: name, item: item, state: "done", msg: msg, updated: time.Now().UnixMilli(),
		}); err != nil {
			return
		}
		g.settleRetry(bg, id, leader, name, item, msg, tainted)
	}()
	return nil
}

// settle 投递一条结果并销账；投递失败则留账，下次 Recover 再投。untrusted：成员回合含不可信输入，
// 结果按不可信投给领导（只降不升）。
func (g *Group) settle(ctx context.Context, member, leader, name, item, msg string, untrusted bool) error {
	if err := g.deliverID(ctx, leader, "member:"+name, item, msg, untrusted); err != nil {
		return err
	}
	return g.deleteRun(ctx, member, item)
}

// settleRetry 投递成员结果，失败后台退避重试，直到成功或进程退出（审查 P1-27：原先失败只留账、
// 要等下次重启 Recover 才补投，长驻进程里领导一直收不到）。账一直留着，退出后由 Recover 接手。
func (g *Group) settleRetry(ctx context.Context, member, leader, name, item, msg string, untrusted bool) {
	wait := settleBackoff
	for {
		if err := g.settle(ctx, member, leader, name, item, msg, untrusted); err == nil {
			return
		}
		select {
		case <-g.ctx.Done():
			return
		case <-time.After(wait):
		}
		if wait < settleMaxBackoff {
			wait *= 2
		}
	}
}

// 结果投递重试的退避（测试可调小）。
var (
	settleBackoff    = time.Second
	settleMaxBackoff = time.Minute
)

// Recover 补投上次进程未投递的成员结果：已完成的投结果；进程退出时仍在跑的，告知领导它被中断。
// 启动时调用一次（在开始派新成员之前）。返回补投条数。
func (g *Group) Recover(ctx context.Context) (int, error) {
	rs, err := g.listRuns(ctx)
	if err != nil {
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
		// 重启后不知道成员回合的可信度：已完成的结果按不可信补投（只降不升）；"中断"通知是本组件写的，可信。
		if err := g.settle(ctx, r.member, r.leader, r.name, r.item, msg, r.state == "done"); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// deliver 把一条消息作为输入投递到会话 inbox（会话未打开则先打开）。
func (g *Group) deliver(ctx context.Context, to, from, text string, untrusted bool) error {
	return g.deliverID(ctx, to, from, host.NewItemID(), text, untrusted)
}

// deliverID 同 deliver，item id 由调用方给（重投幂等）。
func (g *Group) deliverID(ctx context.Context, to, from, item, text string, untrusted bool) error {
	hd, ok := g.Host.Get(to)
	if !ok {
		var err error
		if hd, err = g.Host.Open(ctx, to); err != nil {
			return err
		}
	}
	return hd.Do(host.Command{Kind: host.Send, ItemID: item, Text: text, Source: from, Trust: host.TrustOf(untrusted)})
}

// stopTool team_stop：领导中止一个正在工作的成员的当前回合（审查 P1-27：原先只能等 MemberTimeout）。
// 成员会话保留，之后可以再 team_send 让它继续。权限项沿用 team.send（同属组内协作，比发消息更温和）。
func (g *Group) stopTool() *toolhub.Tool {
	return &toolhub.Tool{
		Spec: session.ToolSpec{Name: "team_stop",
			Description: "中止一个正在工作的成员的当前任务（会话保留，之后可用 team_send 让它继续）。",
			Schema:      obj(`"name":{"type":"string","description":"成员名"}`, "name")},
		Risk:  toolhub.Write,
		Items: []string{ItemSend},
		Call: func(ctx context.Context, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
			var a struct{ Name string }
			if err := json.Unmarshal(raw, &a); err != nil || !nameRe.MatchString(a.Name) {
				return toolhub.Failf("需要合法的 name"), nil
			}
			leader, self := team(cx.SessionID)
			if self != "lead" {
				return toolhub.Failf("只有领导会话能中止成员"), nil
			}
			id := MemberID(leader, a.Name)
			g.mu.Lock()
			busy := g.running[id]
			g.mu.Unlock()
			hd, open := g.Host.Get(id)
			if !busy || !open {
				return toolhub.Text("成员 " + a.Name + " 当前没有在工作"), nil
			}
			if err := hd.Do(host.Command{Kind: host.Cancel}); err != nil {
				return toolhub.Failf("中止失败：%v", err), nil
			}
			return toolhub.Text("已中止成员 " + a.Name + " 的当前任务；它的中断结果会作为一条消息送回"), nil
		},
	}
}

func (g *Group) sendTool() *toolhub.Tool {
	return &toolhub.Tool{
		Spec: session.ToolSpec{Name: "team_send",
			Description: "给工作组里的另一位发消息：to 为 \"lead\"（领导）或成员名。消息进入对方 inbox，对方空闲时立即处理。",
			Schema:      obj(`"to":{"type":"string"},"message":{"type":"string"}`, "to", "message")},
		Risk:  toolhub.Write,
		Items: []string{ItemSend},
		Call: func(ctx context.Context, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
			var a struct{ To, Message string }
			if err := json.Unmarshal(raw, &a); err != nil || strings.TrimSpace(a.Message) == "" {
				return toolhub.Failf("需要 to 与非空 message"), nil
			}
			leader, self := team(cx.SessionID)
			if a.To == self {
				return toolhub.Failf("不能发给自己"), nil
			}
			text := fmt.Sprintf("[来自 %s] %s", self, a.Message)
			if a.To == "lead" {
				if err := g.deliver(ctx, leader, "member:"+self, text, cx.Ctx.Untrusted()); err != nil {
					return toolhub.Failf("发送失败：%v", err), nil
				}
				return toolhub.Text("已发送给 lead"), nil
			}
			if !nameRe.MatchString(a.To) {
				return toolhub.Failf("成员名不合法：%s", a.To), nil
			}
			id := MemberID(leader, a.To)
			ok, err := g.Sessions.Exists(ctx, id)
			if err != nil {
				return toolhub.Output{}, err
			}
			if !ok {
				return toolhub.Failf("没有成员 %s（先用 team_spawn 派出）", a.To), nil
			}
			g.mu.Lock()
			busy := g.running[id]
			g.mu.Unlock()
			if busy { // 回合进行中：进 inbox，当前回合结束后处理（结果随后台回合一并回报）
				hd, ok := g.Host.Get(id)
				if ok {
					if err := hd.Do(host.Command{Kind: host.Send, ItemID: host.NewItemID(), Text: text, Source: "member:" + self,
						Trust: host.TrustOf(cx.Ctx.Untrusted())}); err != nil {
						return toolhub.Failf("发送失败：%v", err), nil
					}
					return toolhub.Text("已发送给 " + a.To + "（它正在工作，处理完当前任务后会看到）"), nil
				}
			}
			if err := g.start(leader, id, a.To, text, cx.Ctx.Untrusted()); err != nil { // 空闲成员：开新回合，完成后结果回报领导
				return toolhub.Failf("%v", err), nil
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

// ErrNoTask 看板上没有这个任务。
var ErrNoTask = errors.New("workgroup: 没有任务")

// taskStatuses 任务状态的全集。List 按 status 索引分段查，这里是拼全量的顺序。
var taskStatuses = []string{"pending", "in_progress", "completed"}

// seqKey 分区内的自增计数器行。与任务行同集合，靠 seqMark 字段区分，List 时跳过。
const seqKey = "__seq__"

// seqMark 计数器行正文里的标记字段，任务行不会有。
const seqMark = "_seq"

// taskKey 任务 id 的集合 key：十进制固定 10 位补零。
// Query 按 Key 字符串升序，不补零的话 10 排在 2 前面，List 的顺序就不是 id 序。
func taskKey(id int) string { return fmt.Sprintf("%010d", id) }

// taskBody 任务行的正文。team 是分区字段，status / owner 是索引字段，写入时必须在。
func taskBody(team string, t Task) map[string]any {
	return map[string]any{
		"team": team, "id": t.ID, "subject": t.Subject, "description": t.Description,
		"owner": t.Owner, "status": t.Status, "result": t.Result,
	}
}

func taskFromDoc(d store.Doc) (Task, bool) {
	if _, ok := d.Body[seqMark]; ok {
		return Task{}, false
	}
	return Task{
		ID: int(numOf(d.Body["id"])), Subject: strOf(d.Body["subject"]),
		Description: strOf(d.Body["description"]), Owner: strOf(d.Body["owner"]),
		Status: strOf(d.Body["status"]), Result: strOf(d.Body["result"]), Rev: int(d.Version),
	}, true
}

func strOf(v any) string {
	s, _ := v.(string)
	return s
}

// numOf 读 JSON 往返后的数字（读回是 float64）或直接写入的整数。
func numOf(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	}
	return 0
}

// nextID 取分区内的下一个任务 id。计数器行与任务行同集合、固定 key，冲突重试。
func (g *Group) nextID(ctx context.Context, team string) (int, error) {
	for {
		d, err := g.Tasks.Get(ctx, team, seqKey)
		if errors.Is(err, store.ErrNotFound) {
			d = store.Doc{Key: seqKey, Body: map[string]any{"team": team, seqMark: true, "n": int64(0), "status": "", "owner": ""}}
		} else if err != nil {
			return 0, err
		}
		n := numOf(d.Body["n"]) + 1
		d.Body["n"] = n
		if err := g.Tasks.Put(ctx, team, d, d.Version); err != nil {
			if errors.Is(err, store.ErrConflict) {
				continue
			}
			return 0, err
		}
		return int(n), nil
	}
}

// Create 新建任务。
func (g *Group) Create(ctx context.Context, team, subject, desc string) (Task, error) {
	id, err := g.nextID(ctx, team)
	if err != nil {
		return Task{}, err
	}
	t := Task{ID: id, Subject: subject, Description: desc, Status: "pending", Rev: 1}
	doc := store.Doc{Key: taskKey(id), Body: taskBody(team, t)}
	if err := g.Tasks.Put(ctx, team, doc, 0); err != nil {
		return Task{}, err
	}
	return t, nil
}

// List 列出任务，按 id 升序。集合没有"列出分区全部"的操作，按 status 索引分段查再归并。
func (g *Group) List(ctx context.Context, team string) ([]Task, error) {
	var out []Task
	for _, st := range taskStatuses {
		page := store.Page{}
		for {
			docs, next, err := g.Tasks.Query(ctx, team, "status", st, page)
			if err != nil {
				return nil, err
			}
			for _, d := range docs {
				if t, ok := taskFromDoc(d); ok {
					out = append(out, t)
				}
			}
			if next.After == "" {
				break
			}
			page = next
		}
	}
	sortTasks(out)
	return out, nil
}

// sortTasks 按 id 升序。分段查各段内部已按 key 有序，这里只归并三段。
func sortTasks(ts []Task) {
	for i := 1; i < len(ts); i++ {
		for j := i; j > 0 && ts[j].ID < ts[j-1].ID; j-- {
			ts[j], ts[j-1] = ts[j-1], ts[j]
		}
	}
}

func (g *Group) get(ctx context.Context, team string, id int) (Task, error) {
	d, err := g.Tasks.Get(ctx, team, taskKey(id))
	if errors.Is(err, store.ErrNotFound) {
		return Task{}, fmt.Errorf("%w #%d", ErrNoTask, id)
	}
	if err != nil {
		return Task{}, err
	}
	t, ok := taskFromDoc(d)
	if !ok {
		return Task{}, fmt.Errorf("%w #%d", ErrNoTask, id)
	}
	return t, nil
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
	t.Owner, t.Status, t.Result = owner, status, result
	doc := store.Doc{Key: taskKey(t.ID), Body: taskBody(team, t)}
	if err := g.Tasks.Put(ctx, team, doc, int64(t.Rev)); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return t, fmt.Errorf("%w：#%d", ErrConflict, t.ID)
		}
		return t, err
	}
	t.Rev++
	return t, nil
}

func (g *Group) boardTools() []*toolhub.Tool {
	js := func(v any) toolhub.Output {
		b, _ := json.Marshal(v)
		return toolhub.Text(string(b))
	}
	wrap := func(t Task, err error) (toolhub.Output, error) {
		if err != nil {
			if errors.Is(err, ErrConflict) || errors.Is(err, ErrNoTask) {
				return toolhub.Failf("%v", err), nil
			}
			return toolhub.Output{}, err
		}
		return js(t), nil
	}
	no := false
	return []*toolhub.Tool{
		{Spec: session.ToolSpec{Name: "task_create", Description: "在工作组看板上新建任务（pending、无负责人）。",
			Schema: obj(`"subject":{"type":"string"},"description":{"type":"string"}`, "subject")},
			Risk: toolhub.Write, Parallel: &no,
			Items: []string{ItemBoard},
			Call: func(ctx context.Context, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
				var a struct{ Subject, Description string }
				if err := json.Unmarshal(raw, &a); err != nil || strings.TrimSpace(a.Subject) == "" {
					return toolhub.Failf("subject 不能为空"), nil
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
			Risk: toolhub.Write, Parallel: &no,
			Items: []string{ItemBoard},
			Call: func(ctx context.Context, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
				var a struct{ ID int }
				if err := json.Unmarshal(raw, &a); err != nil {
					return toolhub.Failf("需要 id"), nil
				}
				leader, self := team(cx.SessionID)
				return wrap(g.Claim(ctx, leader, a.ID, self))
			}},
		{Spec: session.ToolSpec{Name: "task_complete", Description: "把自己负责的任务标为完成，附一句结果。",
			Schema: obj(`"id":{"type":"integer"},"result":{"type":"string"}`, "id")},
			Risk: toolhub.Write, Parallel: &no,
			Items: []string{ItemBoard},
			Call: func(ctx context.Context, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
				var a struct {
					ID     int
					Result string
				}
				if err := json.Unmarshal(raw, &a); err != nil {
					return toolhub.Failf("需要 id"), nil
				}
				leader, self := team(cx.SessionID)
				return wrap(g.Complete(ctx, leader, a.ID, self, a.Result))
			}},
	}
}
