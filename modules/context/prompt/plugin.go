package prompt

// 插件登记（plugin-switch-plan §2.1）：基础系统提示词。内核不写死任何提示词，
// 文本来自配置 text / file，缺省为下面的 Default，并在末尾附工作目录。
// 插件包的提示词片段（runtime/pkgs）各成一个系统块，排在基础提示词之后、AGENTS.md 之前。

import (
	"fmt"
	"os"
	"path/filepath"

	"tenon/runtime/assemble"
	"tenon/runtime/component"
	"tenon/runtime/pkgs"
	"tenon/runtime/session"
)

// Default 缺省系统提示词（原 cmd/tenon/main.go systemPrompt，文字不变以保持缓存）。
const Default = `你是一个在用户本机工作目录中工作的编程助手。
需要了解代码时用 grep（搜内容）/ glob（找文件）/ list_dir 定位，再用 read_file 读（大文件按 offset/limit 分段），再动手；修改已有文件用 edit（精确替换），新建或整体重写才用 write_file；需要运行命令用 shell。
回答简洁，完成后用一两句话说明做了什么。`

// Config 插件配置。
type Config struct {
	Text    string `cfg:"text" description:"系统提示词全文；空 = 缺省文本"`
	File    string `cfg:"file" description:"从文件读系统提示词（相对工作目录）；优先于 text"`
	Workdir bool   `cfg:"workdir" default:"true" description:"末尾附“工作目录：<路径>”"`
}

type plug struct{}

func init() { component.Register("prompt", func() component.Component { return &plug{} }) }

func (*plug) Meta() component.Meta {
	return component.Meta{Name: "prompt", Version: "0.1.0", Default: true, Effects: []string{component.EffectSystemPrompt},
		Requires: []string{assemble.ServiceWorkdir, "?" + pkgs.Service}, Config: &Config{}}
}

// Text 按配置算出系统提示词。
func Text(c Config, root string) (string, error) {
	text := c.Text
	if c.File != "" {
		p := c.File
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return "", fmt.Errorf("prompt: 读 %s: %w", c.File, err)
		}
		text = string(b)
	}
	if text == "" {
		text = Default
	}
	if c.Workdir {
		text += "\n工作目录：" + root
	}
	return text, nil
}

func (*plug) Setup(cx *component.Registrar) error {
	root, err := component.Use[string](cx, assemble.ServiceWorkdir)
	if err != nil {
		return err
	}
	c, _ := cx.Config().(*Config)
	if c == nil {
		c = &Config{Workdir: true}
	}
	text, err := Text(*c, root)
	if err != nil {
		return err
	}
	component.RegisterAt(cx, assemble.SystemBlock, "prompt", assemble.BlockEntry{Order: 0,
		Func: func(*assemble.SessionCx) (*session.SystemBlock, error) {
			return &session.SystemBlock{Text: text, Source: "agent"}, nil
		}})
	// 会话定义的 prompt（框架设计 12 L1：定义 prompt 进系统段）：接在基础提示词之后，
	// 基础提示词里的工作目录与工具用法照样保留。原先没有任何代码读它，定义里写的角色被静默丢弃
	// （2026-11-04 审查 P0-2）。
	component.RegisterAt(cx, assemble.SystemBlock, "prompt-def", assemble.BlockEntry{Order: 1,
		Func: func(sc *assemble.SessionCx) (*session.SystemBlock, error) {
			if sc == nil || sc.Def == nil {
				return nil, nil
			}
			t := sc.Def.PromptText()
			if t == "" {
				return nil, nil
			}
			return &session.SystemBlock{Text: t, Source: "agent-def:" + sc.Def.ID}, nil
		}})
	ps, err := pkgs.From(cx)
	if err != nil {
		return err
	}
	for _, k := range ps {
		if len(k.Prompts) == 0 {
			continue
		}
		t, err := k.PromptText()
		if err != nil {
			return err
		}
		if t == "" {
			continue
		}
		src := "plugin:" + k.Name
		component.RegisterAt(cx, assemble.SystemBlock, "prompt-"+k.Name, assemble.BlockEntry{Order: 5,
			Func: func(*assemble.SessionCx) (*session.SystemBlock, error) {
				return &session.SystemBlock{Text: t, Source: src}, nil
			}})
	}
	return nil
}

func (*plug) Start(*component.Scope) error { return nil }
