// hello：最短的应用。装配、启动、问一句（docs/上手/01）。
//
//	TENON_BASE_URL=https://api.deepseek.com/v1 TENON_MODEL=deepseek-chat TENON_API_KEY=… go run ./examples/hello
//
// main_test.go 用 tenontest 的脚本模型跑同一个 run，不联网。
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"

	"tenon"
)

func main() {
	model := tenon.WithOpenAI(os.Getenv("TENON_BASE_URL"), os.Getenv("TENON_MODEL"), tenon.Env("TENON_API_KEY"))
	if err := run(context.Background(), os.Stdout, "用一句话介绍你自己。", model); err != nil {
		log.Fatal(err)
	}
}

// run 用给定的模型选项问一句，把回答写到 w。
func run(ctx context.Context, w io.Writer, question string, model tenon.Option) error {
	app, err := tenon.New(tenon.Default(), tenon.WithStore(tenon.MemoryStore()), model)
	if err != nil {
		return err
	}
	defer app.Close(ctx)
	if err := app.Start(ctx); err != nil {
		return err
	}
	r, err := app.Ask(ctx, "", question, nil)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, r.Text)
	return err
}
