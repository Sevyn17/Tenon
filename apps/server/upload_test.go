package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"ybase/appapi"
	"ybase/appapi/wire"
)

type memArt struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (a *memArt) Put(_ context.Context, _, _ string, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	h := hex.EncodeToString(sum[:])
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.m == nil {
		a.m = map[string][]byte{}
	}
	a.m[h] = append([]byte(nil), data...)
	return h, nil
}

func (a *memArt) Get(h string) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if d, ok := a.m[h]; ok {
		return d, nil
	}
	return nil, errors.New("不存在")
}

// POST /api/artifacts：令牌、同源 Origin、会话存在、大小上限，正常上传返回 artifact:// 引用、能用 artifact.get 读回。
func TestUpload(t *testing.T) {
	app := newTestApp(t, func(o *appapi.Options) { o.Artifacts, o.Upload = &memArt{}, true })
	if _, err := app.Call(context.Background(), "session.create", json.RawMessage(`{"session":"u"}`)); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(nil)
	port := ts.Listener.Addr().(*net.TCPAddr).Port
	ts.Config.Handler = New(Config{API: app, Token: testToken, Hosts: LoopbackHosts(port)})
	ts.Start()
	t.Cleanup(ts.Close)

	post := func(token, origin, sess string, data []byte) (*http.Response, string) {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		_ = mw.WriteField("session", sess)
		fw, _ := mw.CreateFormFile("file", "a.bin")
		_, _ = fw.Write(data)
		_ = mw.Close()
		req, _ := http.NewRequest("POST", ts.URL+appapi.UploadPath, &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}
	data := bytes.Repeat([]byte("x"), 3<<20) // 超过 artifact.put 的 2 MiB
	if r, _ := post("", ts.URL, "u", data); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无令牌：%d", r.StatusCode)
	}
	if r, _ := post(testToken, "http://evil.example", "u", data); r.StatusCode != http.StatusForbidden {
		t.Fatalf("跨源：%d", r.StatusCode)
	}
	if r, _ := post(testToken, ts.URL, "none", data); r.StatusCode != http.StatusNotFound {
		t.Fatalf("会话不存在：%d", r.StatusCode)
	}
	r, body := post(testToken, ts.URL, "u", data)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("上传：%d %s", r.StatusCode, body)
	}
	var out wire.ArtifactPutResult
	if err := json.Unmarshal([]byte(body), &out); err != nil || !strings.HasPrefix(out.Ref, "artifact://") {
		t.Fatalf("结果：%s %v", body, err)
	}
	got, err := app.Call(context.Background(), "artifact.get", json.RawMessage(`{"session":"u","ref":"`+out.Ref+`","length":16}`))
	if err != nil || got.(wire.ArtifactGetResult).Size != len(data) {
		t.Fatalf("读回：%+v %v", got, err)
	}
	if r, _ := post(testToken, ts.URL, "u", bytes.Repeat([]byte("y"), appapi.MaxUpload+1)); r.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限：%d", r.StatusCode)
	}
	h, _ := app.Call(context.Background(), "hello", json.RawMessage(`{}`))
	if !strings.Contains(strings.Join(h.(wire.HelloResult).Capabilities, ","), "upload") {
		t.Fatalf("能力：%v", h.(wire.HelloResult).Capabilities)
	}
}
