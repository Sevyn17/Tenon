package plugin

// explain（P20）：生效插件、装配顺序、点位登记、层顺序、配置值与来源、插槽来源、degraded。

import (
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
)

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
		fmt.Fprintf(w, "  %s %s%s%s\n", n, m.Version, tag, src)
		if cfg := a.configs[n]; cfg != nil {
			v := reflect.ValueOf(cfg).Elem()
			for i, f := range SchemaOf(cfg) {
				_ = i
				fv := fieldByKey(v, f.Key)
				fmt.Fprintf(w, "      %s = %v（%s）\n", f.Key, fv, orDefault(a.sources[n][f.Key], "零值"))
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
	for _, pt := range []string{"model", "tool"} {
		ls := a.Layers(pt)
		if len(ls) == 0 {
			continue
		}
		fmt.Fprintf(w, "%s 调用栈（由外到内）：\n", pt)
		for _, l := range ls {
			fmt.Fprintf(w, "  %s %s（%s，超时 %s）\n", l.Spec.Position, l.Plugin, failName(l.Spec.Failure), l.Spec.Timeout)
		}
	}
	fmt.Fprintln(w, "点位登记：")
	byPoint := map[string][]string{}
	for _, r := range a.points {
		if r.Point == "slot" || r.Point == "service" || (strings.HasSuffix(r.Point, "_layer") && r.Point != "session_layer") {
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
		if contains(a.metas[n].Provides, key) {
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

func failName(f Failure) string {
	if f == FailClosed {
		return "FailClosed"
	}
	return "FailOpen"
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
