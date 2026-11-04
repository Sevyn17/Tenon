package host

import "ybase/runtime/component"

// 守卫规则合并（04 4.2、08 D4）：任一条 Deny 即拒绝；全部 Abstain 走内核缺省；
// Allow 只放开「往 session: 投递」的缺省限制，放不开联系名单、预算、跳数和租户边界。
//
// Guards 由装配期从组件登记取来（component.Assembly.GuardRules），装配结束后只读。

// evalGuard 跑完该点位的全部规则并合并结论。
func (h *Host) evalGuard(req component.GuardRequest) (component.Verdict, string) {
	verdict, reason := component.Abstain, ""
	for _, r := range h.Guards {
		if r.On != req.Point || r.Check == nil {
			continue
		}
		v, why := r.Check(req)
		switch v {
		case component.Deny:
			if why == "" {
				why = r.Name
			}
			return component.Deny, why
		case component.Allow:
			verdict = component.Allow
			if why != "" {
				reason = why
			}
		}
	}
	return verdict, reason
}
