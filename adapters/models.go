package adapters

// 切换模型（appapi.Models）：models 插件的目录 → models.list；session.settings {model} 前按目录校验。

import (
	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/assemble"
)

// ModelList 实现 appapi.Models。
type ModelList struct{ M *assemble.Models }

// List 目录里的别名与模型；提供方凭证解析不出来的标 available = false（选了会在调用时失败）。
func (l ModelList) List() wire.ModelsListResult {
	cat := l.M.Hub.Catalog()
	out := wire.ModelsListResult{Models: []wire.ModelInfo{}, Default: l.M.Model}
	reason := map[string]string{} // 提供方 → 不可用原因（"" = 可用）
	check := func(p string) string {
		if r, ok := reason[p]; ok {
			return r
		}
		r := ""
		key, ok := cat.ProviderKey(p)
		switch {
		case !ok:
			r = "提供方 " + p + " 未声明"
		case key != "" && l.M.Secrets != nil:
			if _, err := l.M.Secrets.Resolve(key); err != nil {
				r = err.Error()
			}
		}
		reason[p] = r
		return r
	}
	for _, e := range cat.Entries() {
		mi := wire.ModelInfo{ID: e.ID, Alias: e.Alias, Provider: e.Provider, Name: e.Name, ContextWindow: e.ContextWindow,
			Images: e.Images, Reasoning: e.Reasoning, Available: true}
		if e.Reasoning {
			mi.Efforts = wire.ReasoningEfforts
		}
		if e.Provider != "" {
			if r := check(e.Provider); r != "" {
				mi.Available, mi.Reason = false, r
			}
		}
		out.Models = append(out.Models, mi)
	}
	return out
}

// Check id 能在目录里解析（别名、provider/model 或唯一的模型名）。
func (l ModelList) Check(id string) error {
	_, err := l.M.Hub.Catalog().Resolve(id)
	return err
}
