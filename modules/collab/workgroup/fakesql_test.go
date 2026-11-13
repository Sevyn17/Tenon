package workgroup

// 旧表搬迁测试用的 SQL。存储实现已迁出框架，框架里不再有 SQLite 驱动；
// 搬迁逻辑本身留在框架，这里用标准库 database/sql 加一个只认两张旧表的内存驱动来驱动它。
// 真实库上「建表、插入、删表」的往返由产品侧的存储实现验证。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/Sevyn17/tenon/runtime/store"
)

func init() { sql.Register("workgroup-fake", &fakeDriver{}) }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) {
	return &fakeConn{tables: map[string]*fakeTable{}}, nil
}

type fakeConn struct {
	mu     sync.Mutex
	tables map[string]*fakeTable
}

type fakeTable struct {
	cols []string
	rows [][]driver.Value
}

func (c *fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("workgroup-fake: 不支持 Prepare")
}
func (c *fakeConn) Close() error              { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) { return fakeTx{}, nil }

func (c *fakeConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	q := strings.TrimSpace(query)
	switch {
	case strings.HasPrefix(q, "CREATE TABLE"):
		name, cols := parseCreate(q)
		c.tables[name] = &fakeTable{cols: cols}
		return driver.RowsAffected(0), nil
	case strings.HasPrefix(q, "INSERT INTO"):
		name, rows := parseInsert(q)
		t := c.tables[name]
		if t == nil {
			return nil, errors.New("no such table: " + name)
		}
		c.tables[name].rows = append(t.rows, rows...)
		return driver.RowsAffected(len(rows)), nil
	case strings.HasPrefix(q, "DROP TABLE"):
		name := strings.TrimSpace(strings.TrimPrefix(q, "DROP TABLE"))
		if _, ok := c.tables[name]; !ok {
			return nil, errors.New("no such table: " + name)
		}
		delete(c.tables, name)
		return driver.RowsAffected(0), nil
	}
	return nil, errors.New("workgroup-fake: 不支持的语句 " + q)
}

func (c *fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	q := strings.TrimSpace(query)
	from := strings.Index(q, "FROM ")
	if from < 0 {
		return nil, errors.New("workgroup-fake: 不支持的查询 " + q)
	}
	name := strings.TrimSpace(q[from+len("FROM "):])
	t := c.tables[name]
	if t == nil {
		return nil, errors.New("no such table: " + name)
	}
	sel := strings.TrimSpace(strings.TrimPrefix(q[:from], "SELECT"))
	var want []string
	for _, col := range strings.Split(sel, ",") {
		want = append(want, strings.TrimSpace(col))
	}
	idx := map[string]int{}
	for i, col := range t.cols {
		idx[col] = i
	}
	var rows [][]driver.Value
	for _, r := range t.rows {
		row := make([]driver.Value, len(want))
		for i, col := range want {
			row[i] = r[idx[col]]
		}
		rows = append(rows, row)
	}
	return &fakeRows{cols: want, rows: rows}, nil
}

type fakeTx struct{}

func (fakeTx) Commit() error   { return nil }
func (fakeTx) Rollback() error { return nil }

type fakeRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

func parseCreate(q string) (name string, cols []string) {
	rest := strings.TrimSpace(strings.TrimPrefix(q, "CREATE TABLE"))
	i := strings.Index(rest, "(")
	name = strings.TrimSpace(rest[:i])
	body := rest[i+1 : strings.LastIndex(rest, ")")]
	for _, line := range strings.Split(body, ",") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "PRIMARY KEY") {
			continue
		}
		cols = append(cols, strings.Fields(line)[0])
	}
	return name, cols
}

func parseInsert(q string) (name string, rows [][]driver.Value) {
	rest := strings.TrimSpace(strings.TrimPrefix(q, "INSERT INTO"))
	name = strings.Fields(rest)[0]
	v := rest[strings.Index(rest, "VALUES")+len("VALUES"):]
	for _, group := range strings.Split(v, "),") {
		group = strings.Trim(strings.TrimSpace(group), "()")
		var vals []driver.Value
		for _, p := range strings.Split(group, ",") {
			vals = append(vals, coerce(strings.Trim(strings.TrimSpace(p), "'")))
		}
		rows = append(rows, vals)
	}
	return name, rows
}

func coerce(s string) driver.Value {
	if s == "" {
		return s
	}
	n, neg, b := 0, false, s
	if s[0] == '-' {
		neg, b = true, s[1:]
	}
	for _, c := range b {
		if c < '0' || c > '9' {
			return s
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		n = -n
	}
	return int64(n)
}

// fakeStore 是测试用的 store.SQL：Migrate 只记版本号（legacyMigrations 的语句是空的），
// DB() 返回挂着上面那个驱动的连接。
type fakeStore struct {
	db *sql.DB
}

func openFakeStore() store.SQL {
	db, err := sql.Open("workgroup-fake", "")
	if err != nil {
		panic(err)
	}
	db.SetMaxOpenConns(1)
	return &fakeStore{db: db}
}

func (f *fakeStore) DB() *sql.DB                                              { return f.db }
func (f *fakeStore) Dialect() string                                          { return "fake" }
func (f *fakeStore) Migrate(context.Context, string, []store.Migration) error { return nil }
