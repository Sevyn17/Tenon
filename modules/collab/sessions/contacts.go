package sessions

// 联系策略（Mortise docs/76 §6，C1 只定义接口与接入点）：应用以可选服务 service:session-contacts 提供，
// session_start 指定名册里的智能体前先问它。没有提供方时只看定义里的 [use.session-start] agents 名单（docs/76 已定 Q8，即现状）。
// 只管"给谁建会话"；session_send 只沿创建链，不经这个策略。临时智能体（agent 留空）不经此判定。

import (
	"context"

	"github.com/Sevyn17/tenon/runtime/ext"
)

// ContactService 联系策略服务名。
const ContactService = "service:session-contacts"

// ContactPolicy 判定定义 from 的会话能否给定义 to 建会话；返回 nil 放行，否则返回说明原因的错误（原样给模型）。
type ContactPolicy interface {
	CanStart(ctx context.Context, from, to string) error
}

// ContactKey ContactPolicy 的类型化键（Requires 写 ContactKey.Maybe()）。
var ContactKey = ext.NewKey[ContactPolicy](ContactService)

// checkContact 名册智能体的联系判定：p 为 nil（没有提供方）时放行，由 pickDefinition 的名单决定。
func checkContact(ctx context.Context, p ContactPolicy, from, to string) error {
	if p == nil || to == "" || to == Definition {
		return nil
	}
	return p.CanStart(ctx, from, to)
}
