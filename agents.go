package tenon

// 智能体定义与存储的外观（计划/08 3.1）：在代码里写定义（不必写 agent.toml 字符串、不必 import agentdef），
// 不给名字时按"缺省智能体"规则选（计划/08 第五节第 2 条）。

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"tenon/runtime/agentdef"
	"tenon/runtime/assemble"
	"tenon/runtime/ext"
	"tenon/runtime/store"
	"tenon/runtime/store/memstore"
	"tenon/runtime/store/sqlite"
	"tenon/runtime/toolhub"
)

// Agent 一个智能体定义，等同目录里的 agent.toml。
type Agent struct {
	Name        string // 定义名：Ask / NewSession 用它
	Description string
	Prompt      string // 系统提示词
	Model       string // 模型名或别名；空 = 应用的缺省模型
	// Tools 能用的工具名（支持 * 与 ? 通配）；空 = 不限（所有显露给会话的工具都能用）。
	Tools []string
	// Use 组件的使用配置，键是组件名（如 "permission": {"mode": "read-only"}），同 agent.toml 的 [use.<组件>]。
	Use map[string]map[string]any
}

// DefaultAgent 内置缺省智能体的名字：应用一个定义都没给时，不带名字的 Ask / NewSession 用它。
// 它只带 WithTools 加的工具和只读的内置工具，不带跑命令、写文件这类会改东西的工具——要用它们，写一个定义列出来。
const DefaultAgent = "default"

const defaultPrompt = "你是一个乐于助人的助手。需要时调用可用的工具，回答简洁准确。"

// WithAgent 在代码里定义智能体（与 WithAgents 的目录来源可以一起用；同名报错）。
func WithAgent(as ...Agent) Option {
	return func(c *config) error {
		for _, a := range as {
			if strings.TrimSpace(a.Name) == "" {
				return fmt.Errorf("%w: WithAgent 的 Name 为空", ErrInvalid)
			}
			c.inline = append(c.inline, a)
		}
		return nil
	}
}

// WithDefaultAgent 不带名字的 Ask / NewSession 用哪个定义（应用给了多个定义时用）。
func WithDefaultAgent(name string) Option {
	return func(c *config) error {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("%w: WithDefaultAgent 的名字为空", ErrInvalid)
		}
		c.defaultAgent = name
		return nil
	}
}

type manifest struct {
	Format      string                    `toml:"format"`
	ID          string                    `toml:"id"`
	Description string                    `toml:"description,omitempty"`
	Model       string                    `toml:"model,omitempty"`
	Prompt      string                    `toml:"prompt,omitempty"`
	Tools       []string                  `toml:"tools,omitempty"`
	Use         map[string]map[string]any `toml:"use,omitempty"`
}

func putAgent(m *agentdef.MemSource, a Agent) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(manifest{Format: agentdef.FormatV1, ID: a.Name, Description: a.Description,
		Model: a.Model, Prompt: a.Prompt, Tools: a.Tools, Use: a.Use}); err != nil {
		return fmt.Errorf("%w: 定义 %s：%v", ErrInvalid, a.Name, err)
	}
	return m.Put(agentdef.Ref{Namespace: agentdef.Platform, Name: a.Name}, agentdef.Files{agentdef.ManifestName: buf.Bytes()})
}

// appAgentNames 应用给的定义名（WithAgent + WithAgents，不含组件模板与内置缺省），排好序。
func (a *App) appAgentNames(ctx context.Context) ([]string, error) {
	refs, err := agentdef.Visible(ctx, a.appDefs, agentdef.Scope{Tenant: store.Tenant})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range refs {
		if !seen[r.Name] {
			seen[r.Name] = true
			out = append(out, r.Name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// resolveAgent 不带名字时选定义：WithDefaultAgent 指定的 → 应用只有一个定义就用它 → 一个都没有用内置缺省 →
// 多个报错并列出名字。
func (a *App) resolveAgent(ctx context.Context, name string) (string, error) {
	if strings.TrimSpace(name) != "" {
		return name, nil
	}
	if a.defaultAgent != "" {
		return a.defaultAgent, nil
	}
	names, err := a.appAgentNames(ctx)
	if err != nil {
		return "", err
	}
	switch len(names) {
	case 0:
		return DefaultAgent, nil
	case 1:
		return names[0], nil
	}
	return "", fmt.Errorf("%w: 有 %d 个智能体定义（%s），要给名字或用 WithDefaultAgent 指定", ErrInvalid, len(names), strings.Join(names, "、"))
}

// putDefaultAgent 应用一个定义都没给时登记内置缺省智能体（Start 里、组件启动之后：工具这时才齐）。
func (a *App) putDefaultAgent(ctx context.Context) error {
	if names, err := a.appAgentNames(ctx); err != nil || len(names) > 0 {
		return err
	}
	var tools []string
	seen := map[string]bool{}
	add := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			tools = append(tools, n)
		}
	}
	for _, t := range a.appToolNames {
		add(t)
	}
	for _, it := range a.asm.Items(assemble.PointSessionTools) {
		e, ok := it.Impl.(assemble.ToolsEntry)
		if !ok || e.Tools == nil || e.Visible != nil { // 按会话决定显露的（委派、工作组）不进缺省
			continue
		}
		for _, t := range e.Tools() {
			if t != nil && t.Risk == toolhub.Read && !t.Deferred {
				add(t.Spec.Name)
			}
		}
	}
	def := Agent{Name: DefaultAgent, Description: "内置缺省智能体：只带应用的工具与只读工具", Prompt: defaultPrompt, Tools: tools}
	if len(tools) == 0 {
		// 白名单为空在定义里等于"不限"。没有可给的工具时，装了权限组件就把它压到只读档位，
		// 免得剩下的写文件、跑命令类工具被缺省智能体拿到。
		if _, ok := a.asm.Value(ext.ServicePermission); ok {
			def.Use = map[string]map[string]any{"permission": {"mode": "read-only"}}
		}
	}
	return putAgent(a.builtinDefs, def)
}

// —— 存储 ——

// MemoryStore 内存存储（测试、演示、不需要落盘的嵌入场景）。
func MemoryStore() Store { return memstore.New() }

// SQLiteStore 打开（没有就新建）一个 SQLite 会话库；应用 Close 时关闭它。
func SQLiteStore(path string) (Store, error) {
	return sqlite.Open(context.Background(), path, sqlite.Options{})
}
