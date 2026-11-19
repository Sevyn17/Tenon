package tenon

// 文件检查点与钩子的装配顺序（docs/50 M9，lead 定的方案 A）：checkpoint 可选依赖 service:hooks，
// 于是在 hooks 之后登记中间件；会话 Hub 里后登记的在外层，检查点包在钩子外面。
// post_tool_use 格式化器改写的内容算进同一次调用的快照，不记成 drift。
// 放在模块根：标准模块之间（含测试）不能互相 import。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/modules/tools/checkpoint"
	"github.com/Sevyn17/tenon/modules/tools/fs"
	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/callctx"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

type cpLayers []component.ConfigLayer

func (l cpLayers) Layers(context.Context) ([]component.ConfigLayer, error) { return l, nil }

type cpUI struct{}

func (cpUI) Print(string)        {}
func (cpUI) Warn(string, string) {}
func (cpUI) Ask(context.Context, string, []string) (string, error) {
	return "", errors.New("no ui")
}

type cpDB struct {
	db  store.Store
	art toolhub.Artifacts
}

func (h cpDB) DB() store.Store          { return h.db }
func (h cpDB) Blobs() toolhub.Artifacts { return h.art }

func cpSum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestCheckpointWrapsHooks(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db := memstore.New()
	hook := component.ConfigLayer{Name: "user", Origin: component.OriginUser, Trusted: true,
		Values: map[string]any{"hooks": map[string]any{"hook": []map[string]any{
			{"event": "post_tool_use", "matcher": "write_file", "command": "echo formatted> a.txt"}}}}}
	asm, err := component.Assemble(component.Spec{
		Plugins: []component.Entry{{Name: "hooks"}, {Name: checkpoint.Name}},
		Inject: map[string]any{assemble.ServiceWorkdir: root, assemble.ServiceUI: cpUI{},
			assemble.ServiceDB: cpDB{db: db, art: newMemArtifacts(nil)}, component.KeyLayers.Name(): cpLayers{hook}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := asm.OpenCollections(ctx, db); err != nil {
		t.Fatal(err)
	}
	names := asm.MiddlewareNames(component.PointToolCallMw)
	if len(names) != 2 || names[0] != "hooks" || names[len(names)-1] != checkpoint.Name {
		t.Fatalf("checkpoint 应在 hooks 之后登记（= 外层）：%v", names)
	}
	v, _ := asm.Value(checkpoint.ServiceName)
	eng, ok := v.(*checkpoint.Engine)
	if !ok {
		t.Fatalf("service:checkpoint 应为 *Engine：%T", v)
	}
	tr := &fs.Tracker{}
	b := &assemble.Builder{DB: db, ToolCallMW: asm.ToolCallTransforms(),
		Inits: []assemble.InitFunc{func(sc *assemble.SessionCx) error {
			sc.Hub.AddAuthorizer(toolhub.AuthorizerFunc(func(context.Context, *toolhub.Cx, json.RawMessage) (toolhub.Decision, error) {
				return toolhub.Decision{Action: toolhub.DecisionAllow}, nil
			}))
			return nil
		}},
		Tools: []assemble.ToolSet{{Tools: func() []*toolhub.Tool { return []*toolhub.Tool{fs.WriteFile(root, tr)} }}}}
	a, _, err := b.Build("s1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	call := func(id, content string) toolhub.Result {
		args, _ := json.Marshal(map[string]string{"path": "a.txt", "content": content})
		run := a.Hub.ExecuteIn(ctx, a.Toolset, callctx.New(callctx.Options{SessionID: "s1"}),
			[]session.ToolCall{{ID: id, Name: "write_file", Args: args}}, nil)
		res, _ := run.Next()
		return res
	}
	if res := call("c1", "raw content"); res.Output.Status != session.ToolOK {
		t.Fatalf("write_file 应成功：%+v", res.Output)
	}
	disk, err := os.ReadFile(filepath.Join(root, "a.txt"))
	if err != nil || !strings.HasPrefix(string(disk), "formatted") {
		t.Fatalf("钩子应改写文件：%q %v", disk, err)
	}
	eng.Reconcile(ctx, "s1", 0)
	cs, err := eng.List(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].Source != checkpoint.SourceFS || cs[0].After != cpSum(disk) || cs[0].Before != "" {
		t.Fatalf("应只有一条 fs 记录，after = 钩子改写后的内容：%+v", cs)
	}

	// 还原后读后写检查要求重读（spike 第四节：期望行为）。
	if _, err := eng.Revert(ctx, checkpoint.RevertOptions{Session: "s1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("新建文件还原 = 删除：%v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("user typed a longer line"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := call("c2", "x"); res.Output.Status == session.ToolOK {
		t.Fatalf("还原 / 外部改动后应要求先重读：%+v", res.Output)
	}
}
