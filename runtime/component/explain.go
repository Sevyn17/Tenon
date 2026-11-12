package component

// explain（P20）：生效插件、装配顺序、点位登记、层顺序、配置值与来源、插槽来源、degraded。

import (
	"fmt"
	"io"
	"reflect"
	"slices"
	"sort"
	"strings"
)

// PackageInfo 一个已加载插件包的摘要，explain 用（T4d）。
type PackageInfo struct {
	Name     string
	Scope    string   // user / project
	File     string   // 清单路径
	Contents string   // 内容摘要
	Agents   []string // 贡献的定义，如 "platform:helper"
}

// SetPackages 记下已加载的插件包（explain 用）。应用在装配后调。
func (a *Assembly) SetPackages(ps []PackageInfo) {
	a.packages = append([]PackageInfo(nil), ps...)
}

// Explain 把装配结果写成可读文本。profileSources 可为 nil。
func (a *Assembly) Explain(w io.Writer, profileSources map[string]string) {
	fmt.Fprintln(w, "插件（装配顺序）：")
	for _, n := range a.order {
		m := a.metas[n]
		tag := ""
		if a.mandatory[n] {
			tag = " [强制]"
		}
		src := ""
		if s, ok := profileSources[n]; ok {
			src = "  ← " + s
		}
		fmt.Fprintf(w, "  %s %s%s%s%s\n", n, m.Version, tag, rewriteTag(m.Effects), src)
		if cfg := a.configs[n]; cfg != nil {
			v := reflect.ValueOf(cfg).Elem()
			for i, f := range SchemaOf(cfg) {
				_ = i
				fv := fieldByKey(v, f.Key)
				fmt.Fprintf(w, "      %s = %v（%s）\n", f.Key, fv, orDefault(a.sources[n][f.Key], "零值"))
			}
		}
	}
	if len(a.packages) > 0 {
		fmt.Fprintln(w, "插件包：")
		for _, p := range a.packages {
			fmt.Fprintf(w, "  %s（%s）  ← %s\n", p.Name, orDefault(p.Scope, "未知来源"), p.File)
			if p.Contents != "" {
				fmt.Fprintf(w, "      内容：%s\n", p.Contents)
			}
			if len(p.Agents) > 0 {
				fmt.Fprintf(w, "      定义：%s\n", strings.Join(p.Agents, "、"))
			}
		}
	}
	fmt.Fprintln(w, "插槽与服务：")
	keys := make([]string, 0, len(a.values))
	for k := range a.values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		by := "默认实现"
		if a.injected[k] {
			by = "应用预置"
		} else if !a.fromDef[k] {
			by = providerOf(a, k)
		}
		fmt.Fprintf(w, "  %s ← %s\n", k, by)
	}
	fmt.Fprintln(w, "点位登记：")
	byPoint := map[string][]string{}
	for _, r := range a.points {
		if r.Point == "slot" || r.Point == "service" {
			continue
		}
		byPoint[r.Point] = append(byPoint[r.Point], r.Name+"（"+r.Plugin+"）")
	}
	pts := make([]string, 0, len(byPoint))
	for p := range byPoint {
		pts = append(pts, p)
	}
	sort.Strings(pts)
	for _, p := range pts {
		fmt.Fprintf(w, "  %s: %s\n", p, strings.Join(byPoint[p], ", "))
	}
	if d := a.Degraded(); len(d) > 0 {
		fmt.Fprintln(w, "degraded：")
		for n, tasks := range d {
			fmt.Fprintf(w, "  %s: %s\n", n, strings.Join(tasks, ", "))
		}
	}
}

func providerOf(a *Assembly, key string) string {
	for _, n := range a.order {
		if slices.Contains(a.metas[n].Provides, key) {
			return n
		}
	}
	return "?"
}

func fieldByKey(v reflect.Value, key string) any {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).Tag.Get("cfg") == key {
			return v.Field(i).Interface()
		}
	}
	return nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// rewriteTag 标注该组件会不会改写模型所见内容（03 - 约束清单第 19 行：
// 「开启后会改写模型所见内容的插件（如脱敏），要在 explain 和启动横幅里明示」）。
//
// 2026-11-12（H5）：原来 Meta.Effects 的注释承诺「explain / 启动横幅明示」，
// 但两个消费方都不存在——只有 appapi 把它给前端 UI。这里补上 explain 侧。
func rewriteTag(effects []string) string {
	var parts []string
	for _, e := range effects {
		switch e {
		case EffectSystemPrompt:
			parts = append(parts, "改系统提示词")
		case EffectRewriteResult:
			parts = append(parts, "改写模型所见内容")
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "  ⚠ " + strings.Join(parts, "、")
}
