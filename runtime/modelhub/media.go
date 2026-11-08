package modelhub

// 媒体内容（2026-11-04 审查 P2 工具体验：原先图片一律变成"未显示"占位）。会话日志只记媒体引用
// （session.MediaRef，URI = artifact://<sha256>），发请求时由 Request.Media 取回内容；
// 模型目录声明了 caps.images 的端点才发图片，其余照旧留占位文字。

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/Sevyn17/tenon/runtime/session"
)

// MaxImageBytes 单张图片发给模型的上限，超出留占位文字。
const MaxImageBytes = 5 << 20

// Image 一张取回了内容的图片。
type Image struct {
	MIME string
	Data []byte
}

// SplitContent 把内容拆成文本与可发送的图片：端点支持图片、能取回内容、不超上限的 image/* 片段作为图片返回，
// 其余非文本片段在文本里留占位说明。
func SplitContent(r *Request, ep Endpoint, c session.Content) (string, []Image) {
	var sb strings.Builder
	var imgs []Image
	for _, p := range c {
		switch {
		case p.Kind == "text":
			sb.WriteString(p.Text)
		case p.Kind == "":
		default:
			if img, ok := loadImage(r, ep, p); ok {
				imgs = append(imgs, img)
				continue
			}
			mime := ""
			if p.Media != nil {
				mime = p.Media.MIME
			}
			fmt.Fprintf(&sb, "[%s %s 未发送：当前模型不收图片或内容取不回]", p.Kind, mime)
		}
	}
	return sb.String(), imgs
}

func loadImage(r *Request, ep Endpoint, p session.Part) (Image, bool) {
	if !ep.Images || r == nil || r.Media == nil || p.Media == nil || !strings.HasPrefix(p.Media.MIME, "image/") {
		return Image{}, false
	}
	if p.Media.Bytes > MaxImageBytes {
		return Image{}, false
	}
	b, err := r.Media(p.Media.URI)
	if err != nil || len(b) > MaxImageBytes {
		return Image{}, false
	}
	return Image{MIME: p.Media.MIME, Data: b}, true
}

// DataURL 图片的 data URL（data:<mime>;base64,<内容>），OpenAI 两种协议内嵌图片用。
func DataURL(im Image) string {
	return "data:" + im.MIME + ";base64," + base64.StdEncoding.EncodeToString(im.Data)
}
