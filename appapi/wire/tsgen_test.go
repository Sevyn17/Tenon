package wire

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "重新生成 web/src/client/wire.gen.ts")

// web/src/client/wire.gen.ts 与 Go 协议类型一致（06 10.3 G9：不再手写 TS 类型）。
func TestTypeScript(t *testing.T) {
	path := filepath.Join("..", "..", "web", "src", "client", "wire.gen.ts")
	got := TypeScript()
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("缺 %s（先用 -update 生成）：%v", path, err)
	}
	if strings.ReplaceAll(string(want), "\r\n", "\n") != got {
		t.Fatalf("%s 与 Go 协议类型不一致，用 go test ./appapi/wire -run TestTypeScript -update 重新生成", path)
	}
}

// 生成结果的基本形状：键名用 json 标签，可空字段带 ?，RawMessage 成 unknown。
func TestTypeScriptShape(t *testing.T) {
	ts := TypeScript()
	for _, want := range []string{
		"export const PROTOCOL = 2;",
		"export interface Entry {\n  id: string;\n  idx: number;",
		"client_ids?: string[];",
		"args: unknown;",
		"export interface ProtocolError {",
		`export const DurableEvents = ["entry.appended"`,
		"export type ErrorCode = (typeof ErrorCodes)[number];",
	} {
		if !strings.Contains(ts, want) {
			t.Errorf("生成结果缺 %q", want)
		}
	}
	if strings.Contains(ts, "export interface Error {") {
		t.Error("不应生成会遮住内置 Error 的 interface Error")
	}
}
