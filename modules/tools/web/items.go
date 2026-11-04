package web

// 权限项声明（06 - 工具权限设计方案 §9.3）。
//
// web.fetch：match 型，Bounded = true —— 匹配对象是 URL 的主机名，是数据不是程序，
// 规范化后比对即构成边界（工具只会访问参数里的那个 URL）。
// web.search：switch 型，查询词发往搜索服务，没有可匹配的目标对象。

import (
	"encoding/json"
	"net/url"
	"strings"

	"ybase/runtime/toolhub"
)

// 权限项 id。
const (
	ItemFetch  = "web.fetch"
	ItemSearch = "web.search"
)

// Items 本插件声明的权限项。
func Items() []toolhub.Item {
	return []toolhub.Item{
		{
			ID:      ItemFetch,
			Title:   "抓取网页",
			Type:    toolhub.ItemMatch,
			Class:   toolhub.EffectNetwork,
			Bounded: true,
			Subject: fetchSubjects,
			Suggest: map[string]toolhub.ItemAction{
				"read-only": toolhub.ActionAsk,
				"ask":       toolhub.ActionAsk,
				"workspace": toolhub.ActionAsk,
				"full":      toolhub.ActionAllow,
			},
		},
		{
			ID:    ItemSearch,
			Title: "搜索网页",
			Type:  toolhub.ItemSwitch,
			Class: toolhub.EffectNetwork,
			Suggest: map[string]toolhub.ItemAction{
				"read-only": toolhub.ActionAsk,
				"ask":       toolhub.ActionAsk,
				"workspace": toolhub.ActionAsk,
				"full":      toolhub.ActionAllow,
			},
		},
	}
}

// fetchSubjects 从 web_fetch 的参数里取出主机名（小写，去掉端口）。
func fetchSubjects(_ string, args json.RawMessage) ([]string, error) {
	var a struct {
		URL string `json:"url"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, nil // 参数不合法，工具自己会报错
		}
	}
	u, err := url.Parse(strings.TrimSpace(a.URL))
	if err != nil || u.Hostname() == "" {
		return nil, nil
	}
	return []string{strings.ToLower(u.Hostname())}, nil
}
