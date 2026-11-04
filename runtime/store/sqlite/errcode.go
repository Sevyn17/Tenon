package sqlite

// 按驱动的扩展结果码判断约束冲突（2026-11-04 审查 P2 错误一致性：原先用错误文本里的
// "UNIQUE constraint failed" 判断，驱动改措辞就失效）。modernc 的连接都开了扩展结果码。

import (
	"errors"

	sqlite3 "modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"
)

// isUniqueViolation 主键或唯一索引冲突。
func isUniqueViolation(err error) bool {
	var e *sqlite3.Error
	if !errors.As(err, &e) {
		return false
	}
	switch e.Code() {
	case sqlitelib.SQLITE_CONSTRAINT_PRIMARYKEY, sqlitelib.SQLITE_CONSTRAINT_UNIQUE:
		return true
	}
	return false
}
