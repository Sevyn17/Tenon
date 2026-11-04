package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// 两个进程（这里用两个 DB 句柄模拟）同时首次打开同一个新库文件：都应成功，迁移只执行一次。
// 旧行为：版本号在事务外读一次，两边都从 v0 开始迁移，后到者报 BUSY 或「table sessions already exists」。
func TestConcurrentFirstOpen(t *testing.T) {
	const rounds, openers = 10, 3
	for r := 0; r < rounds; r++ {
		path := filepath.Join(t.TempDir(), fmt.Sprintf("s%d.db", r))
		var wg sync.WaitGroup
		errs := make([]error, openers)
		dbs := make([]*DB, openers)
		for i := 0; i < openers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				dbs[i], errs[i] = Open(context.Background(), path, Options{})
			}(i)
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("第 %d 轮第 %d 个打开失败: %v", r, i, err)
			}
		}
		for _, db := range dbs {
			db.Close()
		}
	}
}
