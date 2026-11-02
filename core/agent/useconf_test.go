package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"ybase-kernel/core/agentdef"
	"ybase-kernel/core/callctx"
	"ybase-kernel/core/store"
	"ybase-kernel/core/store/sqlite"
	"ybase-kernel/core/toolhub"
	"ybase-kernel/core/useconf"
)

// memoryUse 记忆组件的使用配置原型（06 6.2 的例子）。
type memoryUse struct {
	Store string        `toml:"store" required:"true"`
	By    string        `toml:"by" enum:"user,tenant,session" default:"user"`
	TTL   time.Duration `toml:"ttl" default:"720h"`
	Top   int           `toml:"top" default:"5" tenant:"true" limit:"min"`
}

// layerSource 租户层与用户层的配置来源；两个 map 按组件名给表。
type layerSource struct {
	tenant map[string]map[string]any
	user   map[string]map[string]any
}

func (s layerSource) Tenant(ctx context.Context, tenant, component string) (map[string]any, error) {
	return s.tenant[component], nil
}

func (s layerSource) User(ctx context.Context, tenant, user, component string) (map[string]any, error) {
	return s.user[component], nil
}

func memSchema(t *testing.T) useconf.Schema {
	t.Helper()
	s, err := useconf.SchemaOf("memory", &memoryUse{})
	if err != nil {
		t.Fatalf("SchemaOf: %v", err)
	}
	return s
}

// useAgent 造一个能落库的会话执行体（工具管理器必需，这里不装工具）。
func useAgent(t *testing.T, db *sqlite.DB, def *agentdef.Agent, schemas []useconf.Schema, src useconf.Source) *Agent {
	t.Helper()
	a := &Agent{
		Model: fakeModel{}, Store: db, SessionID: "s1",
		Ctx:       callctx.New(callctx.Options{Tenant: store.Tenant, SessionID: "s1"}),
		Def:       def,
		Schemas:   schemas,
		UseSource: src,
	}
	a.Hub = toolhub.New(toolhub.Options{Store: ToolStore{DB: db}, OnSpan: a.HubSpan})
	return a
}

// 建会话时把 [use.memory] 按链算好存进 use_config；Ctx 读得到，组件不必自己翻文件。
func TestOpenAsResolvesUseConfig(t *testing.T) {
	db := tempStore(t)
	a := useAgent(t, db,
		&agentdef.Agent{ID: "support", Use: map[string]map[string]any{
			"memory": {"store": "memlib", "top": 20},
		}},
		[]useconf.Schema{memSchema(t)},
		layerSource{
			tenant: map[string]map[string]any{"memory": {"top": 8}},
			user:   map[string]map[string]any{"memory": {"top": 3}},
		})
	if _, err := a.OpenAs(context.Background(), testCfg(), store.SessionMeta{Creator: "u42"}); err != nil {
		t.Fatalf("OpenAs: %v", err)
	}

	l, err := db.Load(context.Background(), "s1", nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var got map[string]map[string]any
	if err := json.Unmarshal(l.Meta.UseConfig, &got); err != nil {
		t.Fatalf("use_config 不是 JSON 对象：%v（%s）", err, l.Meta.UseConfig)
	}
	mem := got["memory"]
	if mem["store"] != "memlib" {
		t.Fatalf("定义层没进去：%v", got)
	}
	// 取严：定义 20、租户 8、会话创建者 3 → 3。
	if top, ok := mem["top"].(float64); !ok || int(top) != 3 {
		t.Fatalf("top 应取严为 3：%v", mem["top"])
	}
	// 组件通过 Ctx 读自己的那份配置。
	var use memoryUse
	if err := a.Ctx.Config("memory", &use); err != nil {
		t.Fatalf("Ctx.Config: %v", err)
	}
	if use.Store != "memlib" || use.Top != 3 || use.By != "user" || use.TTL != 720*time.Hour {
		t.Fatalf("Ctx.Config = %+v", use)
	}
	// 恢复会话时沿用库里那份，不重算。
	if _, err := a.OpenAs(context.Background(), testCfg(), store.SessionMeta{Creator: "u42"}); err != nil {
		t.Fatalf("重开：%v", err)
	}
	if use2, ok := a.Ctx.Use("memory"); !ok || use2 == nil {
		t.Fatal("重开后丢了 use_config")
	}
}

// 用户层不能覆盖没标 tenant:"true" 的字段（D7：越权拒绝）。
func TestOpenAsRejectsUserOverride(t *testing.T) {
	db := tempStore(t)
	a := useAgent(t, db,
		&agentdef.Agent{ID: "support", Use: map[string]map[string]any{"memory": {"store": "memlib"}}},
		[]useconf.Schema{memSchema(t)},
		layerSource{user: map[string]map[string]any{"memory": {"store": "/tmp/other"}}})
	_, err := a.OpenAs(context.Background(), testCfg(), store.SessionMeta{Creator: "u42"})
	if !errors.Is(err, useconf.ErrNotTenant) {
		t.Fatalf("应当拒绝用户层覆盖 store：%v", err)
	}
	// 会话没建起来。
	if _, err := db.Load(context.Background(), "s1", nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("失败时不该留下会话行：%v", err)
	}
}

// 定义里写了没登记使用配置的组件：建会话直接报，不静默忽略。
func TestOpenAsRejectsUnknownComponent(t *testing.T) {
	db := tempStore(t)
	a := useAgent(t, db,
		&agentdef.Agent{ID: "support", Use: map[string]map[string]any{"nope": {"x": 1}}},
		[]useconf.Schema{memSchema(t)}, nil)
	if _, err := a.OpenAs(context.Background(), testCfg(), store.SessionMeta{}); err == nil {
		t.Fatal("未登记的组件应当报错")
	}
}

// 调用方已经给了 use_config（子会话继承）就不再算。
func TestOpenAsKeepsGivenUseConfig(t *testing.T) {
	db := tempStore(t)
	given := json.RawMessage(`{"memory":{"store":"inherited"}}`)
	a := useAgent(t, db,
		&agentdef.Agent{ID: "support", Use: map[string]map[string]any{"memory": {"store": "memlib"}}},
		nil, nil)
	if _, err := a.OpenAs(context.Background(), testCfg(), store.SessionMeta{UseConfig: given}); err != nil {
		t.Fatalf("OpenAs: %v", err)
	}
	l, err := db.Load(context.Background(), "s1", nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(l.Meta.UseConfig) != string(given) {
		t.Fatalf("use_config 被改写：%s", l.Meta.UseConfig)
	}
}
