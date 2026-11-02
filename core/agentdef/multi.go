package agentdef

import (
	"context"
	"errors"
)

// multi 多个来源合成一个（WithAgents 可给多个，07 7.2）：按给的顺序合并，同层同名以先给的为准。
type multi struct{ srcs []Source }

// Multi 合并多个定义来源；nil 来源跳过。
func Multi(srcs ...Source) Source {
	kept := make([]Source, 0, len(srcs))
	for _, s := range srcs {
		if s != nil {
			kept = append(kept, s)
		}
	}
	return &multi{srcs: kept}
}

func (m *multi) List(ctx context.Context, scope Scope) ([]Ref, error) {
	seen := map[Ref]bool{}
	var out []Ref
	for _, s := range m.srcs {
		refs, err := s.List(ctx, scope)
		if err != nil {
			return nil, err
		}
		for _, r := range refs {
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
	}
	sortRefs(out)
	return out, nil
}

func (m *multi) Load(ctx context.Context, ref Ref) (Agent, error) {
	for _, s := range m.srcs {
		a, err := s.Load(ctx, ref)
		if err == nil {
			return a, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Agent{}, err
		}
	}
	return Agent{}, ErrNotFound
}

var _ Source = (*multi)(nil)
