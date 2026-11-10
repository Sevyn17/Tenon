package memstore

// Tx 回滚用的状态快照（2026-11-04 审查 P1-55：原先 Tx 只是执行 fn，失败不回滚，一致性套件约束不了事务语义）。
// 深复制全部可变状态；条目体等 []byte 写入后不再修改，按值共享。

import "github.com/Sevyn17/tenon/runtime/store"

type snapshot struct {
	clock    int64
	sessions map[string]*sess
	shells   map[string][]byte
	defs     map[string][]def
	inbox    map[string][]inboxRow
	tools    map[string]map[string]*toolRow
	pending  map[string]store.Pending
	outbox    []outboxRow
	traces    []store.TraceEvent
	collSpecs map[string]store.CollectionSpec
	collDocs  map[string]map[string]*collDoc
	collVers  map[string]int
}

// snap 复制当前状态；调用方持 s.mu。
func (s *Store) snap() snapshot {
	sn := snapshot{clock: s.clock,
		sessions: make(map[string]*sess, len(s.sessions)),
		shells:   make(map[string][]byte, len(s.shells)),
		defs:     make(map[string][]def, len(s.defs)),
		inbox:    make(map[string][]inboxRow, len(s.inbox)),
		tools:    make(map[string]map[string]*toolRow, len(s.tools)),
		pending:  make(map[string]store.Pending, len(s.pending)),
		outbox:    append([]outboxRow(nil), s.outbox...),
		traces:    append([]store.TraceEvent(nil), s.traces...),
		collSpecs: make(map[string]store.CollectionSpec, len(s.collSpecs)),
		collDocs:  make(map[string]map[string]*collDoc, len(s.collDocs)),
		collVers:  make(map[string]int, len(s.collVers)),
	}
	for k, x := range s.sessions {
		c := *x
		c.entries = make(map[int]entry, len(x.entries))
		for i, e := range x.entries {
			c.entries[i] = e
		}
		c.archive = append([]archived(nil), x.archive...)
		c.requests = make(map[string]request, len(x.requests))
		for i, r := range x.requests {
			c.requests[i] = r
		}
		c.reqOrder = append([]string(nil), x.reqOrder...)
		c.spans = append([]spanRow(nil), x.spans...)
		c.events = append([]evRow(nil), x.events...)
		sn.sessions[k] = &c
	}
	for k, v := range s.shells {
		sn.shells[k] = v
	}
	for k, v := range s.defs {
		sn.defs[k] = append([]def(nil), v...)
	}
	for k, v := range s.inbox {
		sn.inbox[k] = append([]inboxRow(nil), v...)
	}
	for k, m := range s.tools {
		cm := make(map[string]*toolRow, len(m))
		for id, r := range m {
			cr := *r
			cm[id] = &cr
		}
		sn.tools[k] = cm
	}
	for k, v := range s.pending {
		sn.pending[k] = v
	}
	for k, v := range s.collSpecs {
		sn.collSpecs[k] = v
	}
	for k, m := range s.collDocs {
		cm := make(map[string]*collDoc, len(m))
		for id, d := range m {
			cd := *d
			cm[id] = &cd
		}
		sn.collDocs[k] = cm
	}
	for k, v := range s.collVers {
		sn.collVers[k] = v
	}
	return sn
}

// restore 恢复快照；调用方持 s.mu。
func (s *Store) restore(sn snapshot) {
	s.clock, s.sessions, s.shells, s.defs = sn.clock, sn.sessions, sn.shells, sn.defs
	s.inbox, s.tools, s.pending, s.outbox, s.traces = sn.inbox, sn.tools, sn.pending, sn.outbox, sn.traces
	s.collSpecs, s.collDocs, s.collVers = sn.collSpecs, sn.collDocs, sn.collVers
}
