package sqlite

// 并库：把另一个库（旧版本每个项目一个的 <项目>/.tenon/state.db）的会话并进本库，记上所属工作区。
// 先用 Open 打开源库（跑迁移，与本库同一版表结构），再 ATTACH 进本库按列名逐表 INSERT OR IGNORE。
// 本库已有同 id 的会话不导入（连同它在各表里的行），免得两份数据混进一个会话。

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Sevyn17/tenon/runtime/store"
)

// ImportResult 并库结果。
type ImportResult struct {
	Sessions int      // 导入的会话数（含子会话）
	Skipped  []string // 本库已有、没导入的会话 id
}

// Import 把 path 处的库并进本库；源库里没记工作区的会话记成 workspace。源库的数据不改，只会被迁移到当前表结构。
func (db *DB) Import(ctx context.Context, path, workspace string) (ImportResult, error) {
	var res ImportResult
	if db.tx != nil {
		return res, fmt.Errorf("sqlite: 事务视图上不能并库")
	}
	src, err := Open(ctx, path, Options{}) // 跑迁移：两边表结构一致
	if err != nil {
		return res, fmt.Errorf("打开 %s：%w", path, err)
	}
	if err := src.Close(); err != nil {
		return res, err
	}
	conn, err := db.sql.Conn(ctx) // ATTACH 只对一个连接有效
	if err != nil {
		return res, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `ATTACH DATABASE ? AS src`, path); err != nil {
		return res, err
	}
	defer conn.ExecContext(context.Background(), `DETACH DATABASE src`) //nolint:errcheck

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback() //nolint:errcheck

	// 撞 id 的会话：本库已有。
	rows, err := tx.QueryContext(ctx, `SELECT id FROM src.sessions WHERE id IN (SELECT id FROM main.sessions)`)
	if err != nil {
		return res, err
	}
	skip := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return res, err
		}
		skip[id] = true
		res.Skipped = append(res.Skipped, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS import_skip(id TEXT PRIMARY KEY)`); err != nil {
		return res, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM temp.import_skip`); err != nil {
		return res, err
	}
	for id := range skip {
		if _, err := tx.ExecContext(ctx, `INSERT INTO temp.import_skip(id) VALUES(?)`, id); err != nil {
			return res, err
		}
	}

	tables, err := tableNames(ctx, tx, "src")
	if err != nil {
		return res, err
	}
	mainTables, err := tableNames(ctx, tx, "main")
	if err != nil {
		return res, err
	}
	have := map[string]bool{}
	for _, t := range mainTables {
		have[t] = true
	}
	for _, t := range tables {
		if t == "schema_versions" || strings.HasPrefix(t, "sqlite_") || !have[t] {
			continue // 本库没有的表（如本库没开的插件的表）：不建，跳过
		}
		cols, err := commonColumns(ctx, tx, t)
		if err != nil {
			return res, err
		}
		if len(cols) == 0 {
			continue
		}
		list := strings.Join(quoteAll(cols), ", ")
		q := fmt.Sprintf(`INSERT OR IGNORE INTO main.%s(%s) SELECT %s FROM src.%s`, quote(t), list, list, quote(t))
		switch { // 撞 id 的会话：它的行都不导
		case t == "sessions":
			q += ` WHERE id NOT IN (SELECT id FROM temp.import_skip)`
		case contains(cols, "session_id"):
			q += ` WHERE session_id NOT IN (SELECT id FROM temp.import_skip)`
		}
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return res, fmt.Errorf("并表 %s：%w", t, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE main.sessions SET workspace = ? WHERE tenant = ? AND workspace = ''
		AND id IN (SELECT id FROM src.sessions) AND id NOT IN (SELECT id FROM temp.import_skip)`, workspace, store.Tenant); err != nil {
		return res, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM src.sessions WHERE id NOT IN (SELECT id FROM temp.import_skip)`).
		Scan(&res.Sessions); err != nil {
		return res, err
	}
	return res, tx.Commit()
}

func tableNames(ctx context.Context, tx *sql.Tx, schema string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT name FROM %s.sqlite_master WHERE type = 'table'`, schema))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// commonColumns 两边都有的列（按源表的列顺序）。
func commonColumns(ctx context.Context, tx *sql.Tx, table string) ([]string, error) {
	cols := func(schema string) ([]string, error) {
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`PRAGMA %s.table_info(%s)`, schema, quote(table)))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var (
				cid, notnull, pk int
				name, typ        string
				dflt             sql.NullString
			)
			if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				return nil, err
			}
			out = append(out, name)
		}
		return out, rows.Err()
	}
	s, err := cols("src")
	if err != nil {
		return nil, err
	}
	m, err := cols("main")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range s {
		if contains(m, c) {
			out = append(out, c)
		}
	}
	return out, nil
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = quote(s)
	}
	return out
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// ImportDir 把另一个工件目录里本目录没有的文件复制过来（内容寻址，同名即同内容）。
func (b *Blobs) ImportDir(src string) (int, error) {
	n := 0
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasSuffix(p, ".tmp") {
			return nil
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(b.root, rel)
		if _, err := os.Stat(dst); err == nil {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		tmp := dst + ".tmp"
		if err := os.WriteFile(tmp, data, 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, dst); err != nil {
			return err
		}
		n++
		return nil
	})
	return n, err
}
