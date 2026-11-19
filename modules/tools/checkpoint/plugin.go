package checkpoint

// 插件登记（docs/50 M9）：缺省开启。ToolCall 中间件、回合末对账、删会话清理。
//
// 依赖 ?service:hooks 只为排装配顺序：hooks 先 Setup、先登记中间件；会话 Hub 里
// 后登记的中间件在外层（runtime/assemble/toolcallorder_test.go 锁定），于是检查点包在
// 钩子外面，post_tool_use 格式化器的改动算进同一次调用，不记成 drift。

import (
	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// Name 插件名。
const Name = "checkpoint"

// ServiceName 本插件提供的服务（值为 *Engine），下一段的协议命令与 rewind 经它取引擎。
const ServiceName = "service:checkpoint"

// Config 插件配置。
type Config struct {
	MaxFileBytes    int64 `cfg:"max_file_bytes" default:"2097152" title:"单文件上限（字节）" description:"超过只记哈希，不能还原"`
	MaxSessionBytes int64 `cfg:"max_session_bytes" default:"268435456" title:"每会话上限（字节）" description:"本会话新存内容超过后只记哈希"`
	ScanMaxFiles    int   `cfg:"scan_max_files" default:"20000" title:"shell 改动扫描上限（文件数）" description:"shell / MCP / 扩展进程调用前后扫描工作区；文件数超过时不扫，改动列表里注明未跟踪"`
}

// CollectionSpec 集合 checkpoint.changes 的结构。
var CollectionSpec = store.CollectionSpec{Version: 1, Indexes: []string{"session", "path_key"}}

type plug struct{ e *Engine }

func init() { component.Register(Name, func() component.Component { return &plug{} }) }

func (*plug) Meta() component.Meta {
	return component.Meta{Name: Name, Version: "0.1.0", Description: "文件检查点：工具改文件前后留快照，可以查看改动并还原", Default: true, Config: &Config{},
		Requires: []string{assemble.ServiceWorkdir, assemble.ServiceDB, "?" + assemble.ServiceUI, "?service:hooks"},
		Provides: []string{ServiceName}}
}

func (p *plug) Setup(cx *component.Registrar) error {
	cfg, _ := cx.Config().(*Config)
	if cfg == nil {
		cfg = &Config{}
	}
	root, err := component.Use[string](cx, assemble.ServiceWorkdir)
	if err != nil {
		return err
	}
	dbh, err := component.Use[assemble.DBHandle](cx, assemble.ServiceDB)
	if err != nil {
		return err
	}
	coll, err := cx.Collection("changes", CollectionSpec)
	if err != nil {
		return err
	}
	if err := cx.CollectionMigration(component.CollectionMigration{Collection: "changes", From: 0}); err != nil {
		return err
	}
	e := New(root, coll, func() toolhub.Artifacts { return dbh.Blobs() })
	e.MaxFileBytes, e.MaxSessionBytes, e.ScanMaxFiles = cfg.MaxFileBytes, cfg.MaxSessionBytes, cfg.ScanMaxFiles
	if ui, ok, err := component.UseOptional[assemble.UI](cx, assemble.ServiceUI); err != nil {
		return err
	} else if ok && ui != nil {
		e.Notify = func(_, msg string) { ui.Warn("检查点", msg) }
	}
	p.e = e
	component.Wrap(cx, component.ToolCall, e.Middleware())
	component.RegisterAt(cx, assemble.AfterTurn, Name, assemble.AfterTurnFunc(e.AfterTurn))
	component.RegisterAt(cx, assemble.OnSessionDelete, Name, store.Cleaner(e.DeleteSession))
	return cx.Provide("checkpoint", e)
}

func (*plug) Start(*component.Scope) error { return nil }
