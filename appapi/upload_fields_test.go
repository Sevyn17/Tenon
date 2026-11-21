package appapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/appapi/wire"
)

type uploadArtifacts struct {
	puts    int
	session string
	mime    string
}

func (a *uploadArtifacts) Put(_ context.Context, session, mime string, _ []byte) (string, error) {
	a.puts++
	a.session, a.mime = session, mime
	return "hash", nil
}

func (*uploadArtifacts) Get(string) ([]byte, error) {
	return nil, errors.New("不存在")
}

func postUpload(t *testing.T, app *App, session, mimeType string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("session", session); err != nil {
		t.Fatal(err)
	}
	if err := mw.WriteField("mime", mimeType); err != nil {
		t.Fatal(err)
	}
	file, err := mw.CreateFormFile("file", "a.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, UploadPath, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	app.UploadHandler().ServeHTTP(rec, req)
	return rec
}

func TestUploadRejectsOverlongFields(t *testing.T) {
	art := &uploadArtifacts{}
	app := newAppOpts(t, func(o *Options) { o.Artifacts = art })
	if _, err := call2(t, app, "session.create", `{"session":"s"}`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		session string
		mime    string
	}{
		{name: "session超过128字节", session: strings.Repeat("s", 129), mime: "text/plain"},
		{name: "mime超过255字节", session: "s", mime: strings.Repeat("m", 256)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := art.puts
			rec := postUpload(t, app, tc.session, tc.mime)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("状态码 =%d，想要400；body=%s", rec.Code, rec.Body.String())
			}
			var e wire.Error
			if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Code != wire.CodeBadRequest {
				t.Fatalf("应返回 bad_request：%+v err=%v", e, err)
			}
			if art.puts != before {
				t.Fatal("非法字段不应写入工件存储")
			}
		})
	}
}

func TestUploadAcceptsFieldBoundaries(t *testing.T) {
	art := &uploadArtifacts{}
	app := newAppOpts(t, func(o *Options) { o.Artifacts = art })
	session := strings.Repeat("s", 128)
	args, err := json.Marshal(wire.CreateArgs{Session: session})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Call(context.Background(), "session.create", args); err != nil {
		t.Fatal(err)
	}
	mimeType := strings.Repeat("m", 255)
	rec := postUpload(t, app, session, mimeType)
	if rec.Code != http.StatusOK {
		t.Fatalf("边界值应接受：%d %s", rec.Code, rec.Body.String())
	}
	if art.puts != 1 || art.session != session || art.mime != mimeType {
		t.Fatalf("边界字段被截断或改写：puts=%d session=%d字节 mime=%d字节", art.puts, len(art.session), len(art.mime))
	}
}
