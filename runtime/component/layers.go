package component

// 部署期配置的分层来源（18 方案第四节，2026-11-12）。
//
// 内核不读文件、不合并、不规定格式：只定义「一层配置」长什么样。
// 应用经注入提供来源（SDK 是 tenon.WithLayeredConfig），组件经 KeyLayers 取。
// 没注入时组件只用代码里给的值。
//
// 与 useconf.Source 不是一回事：那个是会话级 [use.*] 的租户 / 用户层，每轮按触发者重算；
// 这里是进程启动时定下来的部署期配置（hooks.toml、permissions.toml 这类）。

import (
	"bytes"
	"context"
	"fmt"

	"github.com/BurntSushi/toml"

	"github.com/Sevyn17/tenon/runtime/ext"
)

// KeyLayers 部署期配置来源的服务键。组件在 Requires 里写 KeyLayers.Maybe()，
// 用 GetOptional 取；没注入 = 没有外部配置。
var KeyLayers = ext.NewKey[LayeredConfig](ext.ServiceLayers)

// DecodeValue 把一份通用配置值（ConfigLayer.Values 里的）解码进 out 指向的结构体，
// 字段按 toml 标签对应。v 为 nil 时不动 out。
func DecodeValue(v any, out any) error {
	if v == nil {
		return nil
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(v); err != nil {
		return fmt.Errorf("配置值无法编码：%w", err)
	}
	_, err := toml.Decode(buf.String(), out)
	return err
}

// LayeredConfig 部署期配置的来源。内核不关心它背后是文件、数据库还是远程。
type LayeredConfig interface {
	// Layers 配置层，按「低优先级 → 高优先级」返回。
	Layers(ctx context.Context) ([]ConfigLayer, error)
}

// 来源标记（ConfigLayer.Origin）。脱敏跟来源走，不跟层的位置走：
// 用户来源不脱敏，项目来源脱敏（18 方案第七节）。
const (
	OriginUser    = "user"
	OriginProject = "project"
)

// ConfigLayer 一层配置。
type ConfigLayer struct {
	// Name 层名，explain 和报错用，如 "user"、"project"。
	Name string
	// Origin 来源：OriginUser 或 OriginProject。组件按它决定脱敏，不按层的先后。
	Origin string
	// Trusted 由提供方判定。组件只认这个标记，不自己判断项目受不受信。
	Trusted bool
	// Where 出错时指给用户看的位置（文件路径、"数据库 xxx"）；可空。
	Where string
	// Values 与格式无关的配置值，key 是文件标识（"hooks"、"permissions"、"mcp"）。
	// 值是解码后的通用结构（map、切片、标量），组件按自己的结构体再解码并决定怎么合并。
	Values map[string]any
}

// File 取本层里标识为 key 的那份配置；没有返回 nil。
func (l ConfigLayer) File(key string) any {
	if l.Values == nil {
		return nil
	}
	return l.Values[key]
}
