package adapters

// 切换模型（appapi.Models）：models 插件的目录 → models.list；session.settings {model} 前按目录校验。
// effective（客户端改进 M3，配置T5）：新会话的缺省模型落到哪个提供方、凭据从哪来、由哪项设置决定。

import (
	"strings"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/modelhub"
)

// ModelList 实现 appapi.Models。
type ModelList struct{ M *assemble.Models }

// cred 一个提供方的凭据说明：kind / label 给 effective，problem 非空 = 不可用（原因）。
type cred struct{ kind, label, problem string }

// List 目录里的别名与模型；提供方凭证解析不出来的标 available = false（选了会在调用时失败）。
func (l ModelList) List() wire.ModelsListResult {
	cat := l.M.Hub.Catalog()
	out := wire.ModelsListResult{Models: []wire.ModelInfo{}, Default: l.M.Model}
	seen := map[string]cred{} // 提供方 → 凭据说明（每个提供方只查一次）
	credOf := func(p string) cred {
		if c, ok := seen[p]; ok {
			return c
		}
		c := l.credOf(cat, p)
		seen[p] = c
		return c
	}
	for _, e := range cat.Entries() {
		mi := wire.ModelInfo{ID: e.ID, Alias: e.Alias, Provider: e.Provider, Name: e.Name, Label: e.Label,
			ContextWindow: e.ContextWindow, Images: e.Images, Reasoning: e.Reasoning, Available: true}
		if e.Reasoning {
			mi.Efforts = wire.ReasoningEfforts
		}
		if e.Provider != "" {
			if c := credOf(e.Provider); c.problem != "" {
				mi.Available, mi.Reason = false, c.problem
			}
		}
		out.Models = append(out.Models, mi)
	}
	out.Effective = l.effective(cat, credOf)
	return out
}

// credOf 提供方的凭据说明。Secrets 实现了 assemble.CredentialDescriber 时听它的（产品知道自己的凭据引用格式），
// 否则按缺省规则：空 key 不需要凭据，env: 是环境变量；可用性看 Secrets.Resolve 能否解析（与以前一样）。
func (l ModelList) credOf(cat *modelhub.Catalog, name string) cred {
	p, ok := cat.Provider(name)
	if !ok {
		return cred{kind: "other", problem: "提供方 " + name + " 未声明"}
	}
	if d, ok := l.M.Secrets.(assemble.CredentialDescriber); ok {
		if kind, label, problem, ok := d.DescribeCredential(p.Protocol, p.Key); ok {
			return cred{kind: kind, label: label, problem: problem}
		}
	}
	c := cred{kind: "other", label: p.Key}
	switch {
	case p.Key == "":
		c = cred{kind: "none", label: "不需要凭据"}
	case strings.HasPrefix(p.Key, "env:"):
		c = cred{kind: "env", label: "环境变量 " + strings.TrimPrefix(p.Key, "env:")}
	}
	if p.Key != "" && l.M.Secrets != nil {
		if _, err := l.M.Secrets.Resolve(p.Key); err != nil {
			c.problem = err.Error()
		}
	}
	return c
}

// effective 缺省模型的计划第一项落到哪：提供方、协议、地址、凭据，以及由哪项设置决定（Models.Origin，宿主细分过）。
func (l ModelList) effective(cat *modelhub.Catalog, credOf func(string) cred) *wire.EffectiveModel {
	ef := &wire.EffectiveModel{Model: l.M.Model, Origin: l.M.Origin, OriginRef: l.M.OriginRef, Notice: l.M.Notice,
		CredKind: "other"}
	plan, err := cat.Resolve(l.M.Model)
	if err != nil || len(plan) == 0 {
		ef.Reason = "缺省模型 " + l.M.Model + " 不在目录里"
		return ef
	}
	m, p, err := cat.Lookup(plan[0].Model)
	if err != nil {
		ef.Reason = err.Error()
		return ef
	}
	ef.Provider, ef.Protocol, ef.BaseURL, ef.Name, ef.Label = m.Provider, p.Protocol, p.BaseURL, m.Name, m.Label
	c := credOf(m.Provider)
	ef.CredKind, ef.CredLabel = c.kind, c.label
	ef.Available, ef.Reason = c.problem == "", c.problem
	return ef
}

// Check id 能在目录里解析（别名、provider/model 或唯一的模型名）。
func (l ModelList) Check(id string) error {
	_, err := l.M.Hub.Catalog().Resolve(id)
	return err
}
