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

// maxUploadField是 session/mime表单项的原始读取上限；多读一个字节用于拒绝截断。
const maxUploadField = 256

// UploadHandler 处理 POST multipart 上传（只接受一个 file 字段）。
func (v *App) UploadHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			writeErr(w, http.StatusMethodNotAllowed, Errorf(wire.CodeBadRequest, "only POST is supported"))
			return
		}
		if v.o.Artifacts == nil {
			writeErr(w, http.StatusNotImplemented, Errorf(wire.CodeUnsupported, "no artifact store"))
			return
		}
		mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if mt != "multipart/form-data" {
			writeErr(w, http.StatusUnsupportedMediaType, Errorf(wire.CodeBadRequest, "multipart/form-data required"))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, MaxUpload+1<<20) // 留出表单头部
		mr, err := r.MultipartReader()
		if err != nil {
			writeErr(w, http.StatusBadRequest, Errorf(wire.CodeBadRequest, "failed to parse form: %v", err))
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
				writeErr(w, tooLarge(err), Errorf(wire.CodeInvalid, "upload failed: %v", err))
				return
			}
			switch p.FormName() {
			case "session":
				b, _ := io.ReadAll(io.LimitReader(p, maxUploadField+1))
				if len(b) > maxUploadField {
					writeErr(w, http.StatusBadRequest, Errorf(wire.CodeBadRequest, "session field exceeds %d bytes", maxUploadField))
					return
				}
				sess = strings.TrimSpace(string(b))
			case "mime":
				b, _ := io.ReadAll(io.LimitReader(p, maxUploadField+1))
				if len(b) > maxUploadField {
					writeErr(w, http.StatusBadRequest, Errorf(wire.CodeBadRequest, "mime field exceeds %d bytes", maxUploadField))
					return
				}
				typ = strings.TrimSpace(string(b))
			case "file":
				if data != nil {
					writeErr(w, http.StatusBadRequest, Errorf(wire.CodeBadRequest, "only one file per upload"))
					return
				}
				if typ == "" {
					typ = p.Header.Get("Content-Type")
				}
				data, err = io.ReadAll(io.LimitReader(p, MaxUpload+1))
				if err != nil {
					writeErr(w, tooLarge(err), Errorf(wire.CodeInvalid, "failed to read file: %v", err))
					return
				}
			}
			_ = p.Close()
		}
		if err := need(sess); err != nil {
			writeErr(w, http.StatusBadRequest, AsError(err))
			return
		}
		if err := validID(sess); err != nil {
			writeErr(w, http.StatusBadRequest, Errorf(wire.CodeBadRequest, "%v", err))
			return
		}
		if err := v.exists(r.Context(), sess); err != nil {
			e := AsError(err)
			writeErr(w, http.StatusNotFound, e)
			return
		}
		if len(data) == 0 || len(data) > MaxUpload {
			writeErr(w, http.StatusRequestEntityTooLarge, invalid("file size %d bytes must be between 1 and %d", len(data), MaxUpload))
			return
		}
		if len(typ) > 255 {
			writeErr(w, http.StatusBadRequest, Errorf(wire.CodeBadRequest, "mime exceeds 255 bytes"))
			return
		}
		if typ == "" {
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
