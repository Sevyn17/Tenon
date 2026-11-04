package ybase_test

import (
	"context"
	"errors"
	"testing"

	"ybase"
	"ybase/runtime/assemble"
	"ybase/runtime/component"
)

// flaky 第一次 ready 失败；记录 Start 次数与作用域是否被关。
type flaky struct {
	starts, closes, readies int
}

func (f *flaky) Name() string { return "flaky" }
func (f *flaky) Setup(cx *component.Registrar) error {
	cx.Register(assemble.PointReady, "flaky", assemble.ReadyFunc(func(context.Context) error {
		f.readies++
		if f.readies == 1 {
			return errors.New("第一次失败")
		}
		return nil
	}))
	return nil
}
func (f *flaky) Start(sc *component.Scope) error {
	f.starts++
	sc.OnClose(func(context.Context) error { f.closes++; return nil })
	return nil
}

// Start 中途失败要回滚：组件作用域关闭，再 Start 从头来（审查 P1-23：原先第一次的作用域留着，组件启动两次不关）。
func TestStartFailureRollsBack(t *testing.T) {
	f := &flaky{}
	app, err := ybase.New(append(base(t), ybase.With(f))...)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(context.Background())
	if err := app.Start(context.Background()); err == nil {
		t.Fatal("第一次 Start 应失败")
	}
	if f.closes != 1 {
		t.Fatalf("失败后组件作用域应已关闭：starts=%d closes=%d", f.starts, f.closes)
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("第二次 Start 应成功：%v", err)
	}
	if f.starts != 2 || f.closes != 1 {
		t.Fatalf("每次 Start 各启动一次、失败的那次已关：starts=%d closes=%d", f.starts, f.closes)
	}
}
