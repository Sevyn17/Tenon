package appapi

// 大附件上传（06 10.9）：超过帧上限（MaxArtifactPut）的文件走 HTTP POST multipart/form-data，
// 字段 session、file（可带 mime；缺省取文件部件的 Content-Type）。返回 {ref}，与 artifact.put 同形。
// 鉴权、Origin、Host 检查由挂载它的传输层负责（网页服务、桌面端资源服务器）；这里只管大小、会话与存储。

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// MaxUpload HTTP 上传的单文件上限。
const MaxUpload = 64 << 20

// UploadPath 上传的路径（网页服务与桌面端相同）。
const UploadPath = "/api/artifacts"

// UploadHandler 处理 POST multipart 上传（只接受一个 file 字段）。
func (v *App) UploadHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			writeErr(w, http.StatusMethodNotAllowed, Errorf(wire.CodeBadRequest, "只支持 POST"))
			return
		}
		if v.o.Artifacts == nil {
			writeErr(w, http.StatusNotImplemented, Errorf(wire.CodeUnsupported, "没有工件存储"))
			return
		}
		mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if mt != "multipart/form-data" {
			writeErr(w, http.StatusUnsupportedMediaType, Errorf(wire.CodeBadRequest, "需要 multipart/form-data"))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, MaxUpload+1<<20) // 留出表单头部
		mr, err := r.MultipartReader()
		if err != nil {
			writeErr(w, http.StatusBadRequest, Errorf(wire.CodeBadRequest, "表单解析失败：%v", err))
			return
		}
		var sess, typ string
		var data []byte
		for {
			p, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				writeErr(w, tooLarge(err), Errorf(wire.CodeInvalid, "上传失败：%v", err))
				return
			}
			switch p.FormName() {
			case "session":
				b, _ := io.ReadAll(io.LimitReader(p, 256))
				sess = strings.TrimSpace(string(b))
			case "mime":
				b, _ := io.ReadAll(io.LimitReader(p, 256))
				typ = strings.TrimSpace(string(b))
			case "file":
				if data != nil {
					writeErr(w, http.StatusBadRequest, Errorf(wire.CodeBadRequest, "一次只能传一个文件"))
					return
				}
				if typ == "" {
					typ = p.Header.Get("Content-Type")
				}
				data, err = io.ReadAll(io.LimitReader(p, MaxUpload+1))
				if err != nil {
					writeErr(w, tooLarge(err), Errorf(wire.CodeInvalid, "读取文件失败：%v", err))
					return
				}
			}
			_ = p.Close()
		}
		if err := v.exists(r.Context(), sess); err != nil {
			e := AsError(err)
			writeErr(w, http.StatusNotFound, e)
			return
		}
		if len(data) == 0 || len(data) > MaxUpload {
			writeErr(w, http.StatusRequestEntityTooLarge, invalid("文件大小 %d 字节，须在 1 到 %d 之间", len(data), MaxUpload))
			return
		}
		if typ == "" || len(typ) > 255 {
			typ = "application/octet-stream"
		}
		h, err := v.o.Artifacts.Put(r.Context(), sess, typ, data)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, AsError(err))
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(wire.ArtifactPutResult{Ref: toolhub.ArtifactPrefix + h})
	})
}

func tooLarge(err error) int {
	var mb *http.MaxBytesError
	if errors.As(err, &mb) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

func writeErr(w http.ResponseWriter, code int, e *Error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(e)
}
