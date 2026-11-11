package web

// T6a 跨机器事件续读：按序号从对端拉事件（补 T4b 留的口）。

import (
	"context"
	"net/http/httptest"
	"testing"

	hostpkg "github.com/Sevyn17/tenon/runtime/host"
)

// 对端返回 seq > after 的事件；客户端按 after 续读，不漏不重。
func TestRemoteEventsFetch(t *testing.T) {
	all := []hostpkg.RemoteEvent{
		{Seq: 1, Source: "board", Name: "ticket.created", Data: []byte(`{"id":"a"}`)},
		{Seq: 2, Source: "board", Name: "ticket.created", Data: []byte(`{"id":"b"}`)},
		{Seq: 3, Source: "guard", Name: "guard.denied", Data: []byte(`{}`)},
	}
	read := func(ctx context.Context, after int64, limit int) ([]hostpkg.RemoteEvent, error) {
		var out []hostpkg.RemoteEvent
		for _, e := range all {
			if e.Seq > after {
				out = append(out, e)
				if limit > 0 && len(out) >= limit {
					break
				}
			}
		}
		return out, nil
	}
	srv := httptest.NewServer(RemoteEventsHandler(read))
	defer srv.Close()

	c := &RemoteClient{}
	ctx := context.Background()

	// 第一次：从 0 拉，应拿到 3 条。
	evs, err := c.FetchEvents(ctx, srv.URL, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 || evs[0].Seq != 1 || evs[2].Seq != 3 {
		t.Fatalf("首次拉取 = %+v", evs)
	}
	// 续读：从 1 之后拉，应只拿到 2、3。
	evs2, err := c.FetchEvents(ctx, srv.URL, 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs2) != 2 || evs2[0].Seq != 2 {
		t.Fatalf("从 1 续读 = %+v", evs2)
	}
	// 再续读：从 3 之后拉，应为空。
	evs3, err := c.FetchEvents(ctx, srv.URL, 3, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs3) != 0 {
		t.Fatalf("已到末尾应为空：%+v", evs3)
	}
	// limit 生效。
	evs4, err := c.FetchEvents(ctx, srv.URL, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs4) != 2 {
		t.Fatalf("limit=2 应只拿 2 条：%+v", evs4)
	}
}

// Host.FetchRemoteEvents 没配 fetcher 时报不支持。
func TestFetchRemoteEventsNoFetcher(t *testing.T) {
	h := &hostpkg.Host{}
	if _, err := h.FetchRemoteEvents(context.Background(), "peer", 0, 10); err == nil {
		t.Fatal("没配 fetcher 应报错")
	}
}

// Host.FetchRemoteEvents 配了 fetcher 时转发给它。
func TestFetchRemoteEventsViaHost(t *testing.T) {
	srv := httptest.NewServer(RemoteEventsHandler(func(ctx context.Context, after int64, limit int) ([]hostpkg.RemoteEvent, error) {
		return []hostpkg.RemoteEvent{{Seq: after + 1, Source: "s", Name: "n"}}, nil
	}))
	defer srv.Close()
	h := &hostpkg.Host{RemoteEvents: &RemoteClient{}}
	evs, err := h.FetchRemoteEvents(context.Background(), srv.URL, 41, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Seq != 42 {
		t.Fatalf("应续读到 seq 42：%+v", evs)
	}
}
