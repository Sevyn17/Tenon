package bind

// D-11：SwapAPI 的竞态。原先「StopEvents → 解锁 → 设 s.api」之间进来的 Call 会在旧的、已关闭的 App 上
// 建连接并存下，之后一直连在旧运行时上。现在换 api 与丢弃旧连接在同一把锁里，connection 还会检查连接
// 是不是在当前 App 上建的。

import (
	"encoding/json"
	"slices"
	"sync"
	"testing"

	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/bus"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
)

// appAt 只用来回 hello 的最小 App（workdir 区分是哪一个）。
func appAt(t *testing.T, wd string) *appapi.App {
	t.Helper()
	db := memstore.New()
	b := bus.New(0)
	t.Cleanup(func() { b.Close(); db.Close() })
	app, err := appapi.New(appapi.Options{Host: &host.Host{DB: db, Bus: b}, Bus: b, DB: db, Model: "m", Workdir: wd})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func helloWorkdir(t *testing.T, s *Service) string {
	t.Helper()
	out, err := s.Call("hello", "")
	if err != nil {
		t.Fatalf("hello：%v", err)
	}
	var r wire.HelloResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	return r.Workdir
}

// 竞态窗口里留下的「旧 App 上的连接」不会被沿用：下一次 Call 发现连接不属于当前 App 就重建。
func TestSwapAPIStaleConn(t *testing.T) {
	a, b := appAt(t, "old"), appAt(t, "new")
	s := NewService(a, func(string, any) {})
	if wd := helloWorkdir(t, s); wd != "old" {
		t.Fatalf("换之前应连 old：%s", wd)
	}
	s.SwapAPI(b)
	// 模拟原来的竞态结果：换完之后 s.conn 却是在旧 App 上建的
	s.mu.Lock()
	s.conn, s.connFor = a.NewConn(t.Context(), appapi.LocalPrincipal), a
	s.mu.Unlock()
	if wd := helloWorkdir(t, s); wd != "new" {
		t.Fatalf("换之后应连 new，实际 %s（连接留在了旧 App 上）", wd)
	}
	s.StopEvents()
}

// 客户端改进 M5：SwapAPI 之后经事件回调发 ReconnectedEvent（单独的事件名，不混进 EventName），
// 而且是在新连接、事件转发就位之后发；hello 报 reconnect_event。
func TestSwapAPIEmitsReconnected(t *testing.T) {
	a, b := appAt(t, "old"), appAt(t, "new")
	var mu sync.Mutex
	var got []string
	var s *Service
	var onNew []bool // 每次收到通知时，转发是否已在新 App 的连接上
	s = NewService(a, func(name string, data any) {
		if name == EventName {
			return
		}
		if name == ReconnectedEvent {
			if _, ok := data.(struct{}); !ok {
				t.Errorf("通知数据应为空对象，实际 %T", data)
			}
			s.mu.Lock()
			ok := s.cancel != nil && s.connFor == b
			s.mu.Unlock()
			mu.Lock()
			onNew = append(onNew, ok)
			mu.Unlock()
		}
		mu.Lock()
		got = append(got, name)
		mu.Unlock()
	})
	var h wire.HelloResult
	out, err := s.Call("hello", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &h); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(h.Capabilities, appapi.CapReconnectEvent) {
		t.Fatalf("hello 应报 %s：%v", appapi.CapReconnectEvent, h.Capabilities)
	}
	s.StartEvents()
	mu.Lock()
	if len(got) != 0 {
		t.Fatalf("没换 App 之前不该有通知：%v", got)
	}
	mu.Unlock()

	s.SwapAPI(b)
	mu.Lock()
	if !slices.Equal(got, []string{ReconnectedEvent}) || !slices.Equal(onNew, []bool{true}) {
		t.Fatalf("SwapAPI 后应恰好收到一次 %s，且此时转发已在新连接上：got=%v onNew=%v", ReconnectedEvent, got, onNew)
	}
	mu.Unlock()
	if wd := helloWorkdir(t, s); wd != "new" {
		t.Fatalf("通知之后 hello 应落在新 App：%s", wd)
	}
	out, err = s.Call("hello", "")
	if err != nil {
		t.Fatal(err)
	}
	h = wire.HelloResult{}
	_ = json.Unmarshal([]byte(out), &h)
	if !slices.Contains(h.Capabilities, appapi.CapReconnectEvent) {
		t.Fatalf("换 App 后的新连接也应报 %s：%v", appapi.CapReconnectEvent, h.Capabilities)
	}
	s.StopEvents()

	// 没开事件转发时也发（桌面端总会先 StartEvents，这里只保证不漏）。
	s2 := NewService(a, func(name string, _ any) {
		mu.Lock()
		got = append(got, name)
		mu.Unlock()
	})
	mu.Lock()
	got = nil
	mu.Unlock()
	s2.SwapAPI(b)
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(got, []string{ReconnectedEvent}) {
		t.Fatalf("没开转发时 SwapAPI 也应通知：%v", got)
	}
}

// 换 App 的同时有并发调用、事件转发在跑：换完之后命令和转发都在新 App 上。
func TestSwapAPIConcurrentCalls(t *testing.T) {
	a, b := appAt(t, "old"), appAt(t, "new")
	s := NewService(a, func(string, any) {})
	s.StartEvents()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = s.Call("hello", "")
				}
			}
		}()
	}
	for i := 0; i < 20; i++ {
		if i%2 == 0 {
			s.SwapAPI(b)
		} else {
			s.SwapAPI(a)
		}
	}
	s.SwapAPI(b)
	close(stop)
	wg.Wait()
	if wd := helloWorkdir(t, s); wd != "new" {
		t.Fatalf("最后换上的是 new，实际 %s", wd)
	}
	s.mu.Lock()
	running, onNew := s.cancel != nil, s.connFor == b
	s.mu.Unlock()
	if !running || !onNew {
		t.Fatalf("事件转发应在新 App 的连接上：running=%v onNew=%v", running, onNew)
	}
	s.StopEvents()
}
