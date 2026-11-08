package memstore

// 交互（protocol 2，06 10.6）：与 sqlite 同语义。登记与结束在一把锁内同时改状态与事件序列；
// 子会话的交互同时写进根会话的事件序列。

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/Sevyn17/tenon/runtime/bus"
	"github.com/Sevyn17/tenon/runtime/store"
)

// OpenInteraction 登记一次交互并写 interaction_opened 事件。
func (s *Store) OpenInteraction(ctx context.Context, p store.Pending, epoch int64) error {
	if p.Corr == "" || p.SessionID == "" || p.InteractionID == "" {
		return fmt.Errorf("memstore: 交互登记缺 corr、session_id 或 interaction_id")
	}
	if err := s.lock(); err != nil {
		return err
	}
	x := s.sessions[p.SessionID]
	if x == nil {
		s.mu.Unlock()
		return store.ErrNotFound
	}
	if epoch != 0 && x.leaseEpoch != epoch {
		s.mu.Unlock()
		return fmt.Errorf("%w: 期望纪元 %d，实际 %d", store.ErrLeaseLost, epoch, x.leaseEpoch)
	}
	if _, ok := s.pending[p.Corr]; ok {
		s.mu.Unlock()
		return fmt.Errorf("memstore: corr %s 已登记", p.Corr)
	}
	for _, q := range s.pending {
		if q.InteractionID == p.InteractionID {
			s.mu.Unlock()
			return fmt.Errorf("memstore: 交互 %s 已登记", p.InteractionID) // sqlite 唯一索引
		}
	}
	now := s.now()
	p.CreatedAt, p.Used, p.Outcome = now, false, ""
	p.Payload = cloneRaw(p.Payload)
	s.pending[p.Corr] = p
	data := p.Payload
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	}
	evs := s.interactionEventsLocked(p, store.EventInteractionOpened, data, now)
	s.mu.Unlock()
	s.notify(evs)
	return nil
}

// ResolveInteraction 结束一次交互：只有第一次生效，corr 同时标记已用。
func (s *Store) ResolveInteraction(ctx context.Context, id string, r store.Resolution) (store.Pending, bool, error) {
	if err := s.lock(); err != nil {
		return store.Pending{}, false, err
	}
	corr, p, ok := s.findInteractionLocked(id)
	if !ok {
		s.mu.Unlock()
		return store.Pending{}, false, store.ErrNotFound
	}
	if p.Outcome != "" {
		s.mu.Unlock()
		return p, false, nil
	}
	now := s.now()
	p.Outcome, p.ResolvedAt, p.By, p.ClientID, p.Answer, p.Used = r.Outcome, now, r.By, r.ClientID, r.Answer, true
	s.pending[corr] = p
	d, _ := json.Marshal(map[string]any{"id": id, "session": p.SessionID, "outcome": p.Outcome, "by": p.By})
	evs := s.interactionEventsLocked(p, store.EventInteractionResolved, d, now)
	s.mu.Unlock()
	s.notify(evs)
	return p, true, nil
}

// Interaction 读一次交互。
func (s *Store) Interaction(ctx context.Context, id string) (store.Pending, error) {
	if err := s.lock(); err != nil {
		return store.Pending{}, err
	}
	defer s.mu.Unlock()
	_, p, ok := s.findInteractionLocked(id)
	if !ok {
		return store.Pending{}, store.ErrNotFound
	}
	return p, nil
}

// OpenInteractions 未结束的交互（按登记时间）。
func (s *Store) OpenInteractions(ctx context.Context, session string) ([]store.Pending, error) {
	if err := s.lock(); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	var out []store.Pending
	for _, p := range s.pending {
		if p.InteractionID == "" || p.Outcome != "" {
			continue
		}
		if session != "" && p.SessionID != session && p.Root != session {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].InteractionID < out[j].InteractionID
	})
	return out, nil
}

func (s *Store) findInteractionLocked(id string) (string, store.Pending, bool) {
	if id == "" {
		return "", store.Pending{}, false
	}
	for corr, p := range s.pending {
		if p.InteractionID == id {
			return corr, p, true
		}
	}
	return "", store.Pending{}, false
}

// interactionEventsLocked 把一条交互事件写进发起会话的序列；有根会话（且还在）时再写一份进根会话。调用方持锁。
func (s *Store) interactionEventsLocked(p store.Pending, kind string, data json.RawMessage, now int64) []bus.Envelope {
	ids := []string{p.SessionID}
	if p.Root != "" && p.Root != p.SessionID {
		ids = append(ids, p.Root)
	}
	var evs []bus.Envelope
	for _, sid := range ids {
		x := s.sessions[sid]
		if x == nil {
			continue
		}
		seq := int64(x.nextSeq)
		x.events = append(x.events, evRow{cursor: seq, kind: kind, data: cloneRaw(data), at: now})
		x.nextSeq++
		evs = append(evs, bus.Envelope{V: bus.Version, Seq: seq, At: now, Session: sid, Kind: kind,
			Data: json.RawMessage(append([]byte(nil), data...))})
	}
	return evs
}
