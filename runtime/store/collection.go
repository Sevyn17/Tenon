package store

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
)

// 集合（框架设计 05 5.6，T4a）。
//
// 不在 Store 上：第三方存储实现 Store 即可，集合是可选能力（与 PruneStore、DefinitionStore 同一规则，
// 2026-11-04 审查 P1-53）。没实现 CollectionStore 的存储，登记了集合的组件启动时报"存储不支持集合"。

// MaxCollectionBody 单条文档正文的上限（约束清单「集合」）。
const MaxCollectionBody = 1 << 20

// CollectionPageDefault / CollectionPageMax 是 Query 的分页缺省与上限（约束清单「集合」）。
const (
	CollectionPageDefault = 100
	CollectionPageMax     = 1000
)

// CollectionNameMax 集合名与索引名的最大长度（约束清单「集合」）。
const CollectionNameMax = 32

// CollectionSpec 一个集合的结构。同名再次 Open 时必须与已登记的完全相同。
type CollectionSpec struct {
	Version int // 结构版本；0 视作 1
	// Indexes 可等值查询的字段，必须是写入时 Doc.Body 里存在的键。
	Indexes []string
	// Partition 分区字段，缺省 "tenant"。Put 时 Body[Partition] 必须等于 part。
	Partition string
}

// Doc 一条集合文档。
type Doc struct {
	Key string
	// Version 从 1 起，每次 Put 成功后由存储加一。调用方传入的值只在 ifVersion 里生效。
	Version int64
	// Body 含 Partition 字段。JSON 往返：数字读回是 float64，调用方自己转换。
	Body map[string]any
}

// Page 分页。After 是上一页最后一条的 Key，空 = 从头。
type Page struct {
	After string
	Limit int
}

// Collection 一个已打开的集合。part 是分区值（租户由框架填，不在这里）。
//
// Query 只做等值查询，不提供"列出一个分区的全部"：要全量就声明一个恒为固定值的索引字段。
// 返回的 Page.After 为空表示没有下一页。
type Collection interface {
	Get(ctx context.Context, part, key string) (Doc, error)
	// Put 条件写。ifVersion = 0 表示必须不存在；其他值必须等于当前 Version。
	// 不符返回 ErrConflict，集合不存在该 key 且 ifVersion 非 0 返回 ErrNotFound。
	Put(ctx context.Context, part string, doc Doc, ifVersion int64) error
	// Delete 条件删。ifVersion 必须等于当前 Version；0 不表示"不看版本直接删"，
	// 用于删除一个尚未写入的 key，必定返回 ErrNotFound。
	Delete(ctx context.Context, part, key string, ifVersion int64) error
	// Query 按声明的索引等值查，按 Key 升序。index 不在 Indexes 里返回错误。
	Query(ctx context.Context, part, index string, eq any, page Page) ([]Doc, Page, error)
}

// CollectionStore 提供集合的存储实现它。
type CollectionStore interface {
	// Open 打开或登记一个集合。同名再次打开时 spec 必须相同，否则返回错误，原数据不变。
	Open(ctx context.Context, tenant, owner, name string, spec CollectionSpec) (Collection, error)
}

// collectionName 集合名与索引名的字符集（约束清单「集合」）。
var collectionName = regexp.MustCompile(`^[a-z0-9_]+$`)

// Normalize 补缺省并校验。两个实现的 Open 都先走它，规则只写一处。
func (s CollectionSpec) Normalize() (CollectionSpec, error) {
	if s.Version == 0 {
		s.Version = 1
	}
	if s.Version < 1 {
		return s, fmt.Errorf("store: 集合结构版本 %d 不合法", s.Version)
	}
	if s.Partition == "" {
		s.Partition = "tenant"
	}
	if err := checkCollectionName("分区字段", s.Partition); err != nil {
		return s, err
	}
	seen := map[string]bool{}
	for _, ix := range s.Indexes {
		if err := checkCollectionName("索引", ix); err != nil {
			return s, err
		}
		if seen[ix] {
			return s, fmt.Errorf("store: 索引 %q 重复", ix)
		}
		seen[ix] = true
	}
	return s, nil
}

func checkCollectionName(what, name string) error {
	if !collectionName.MatchString(name) || len(name) > CollectionNameMax {
		return fmt.Errorf("store: %s %q 不合法（小写字母、数字、下划线，最长 %d）", what, name, CollectionNameMax)
	}
	return nil
}

// CheckCollectionID 校验租户、组件名、集合名。Open 用。
func CheckCollectionID(tenant, owner, name string) error {
	if tenant == "" {
		return fmt.Errorf("store: 集合缺租户")
	}
	if err := checkCollectionName("组件名", owner); err != nil {
		return err
	}
	return checkCollectionName("集合名", name)
}

// SameSpec 两份结构是否相同（同名再次 Open 时比）。
func SameSpec(a, b CollectionSpec) bool {
	if a.Version != b.Version || a.Partition != b.Partition || len(a.Indexes) != len(b.Indexes) {
		return false
	}
	for i := range a.Indexes {
		if a.Indexes[i] != b.Indexes[i] {
			return false
		}
	}
	return true
}

// PageLimit 把分页上限归一。0 取缺省，超出取最大。
func PageLimit(n int) int {
	if n <= 0 {
		return CollectionPageDefault
	}
	if n > CollectionPageMax {
		return CollectionPageMax
	}
	return n
}

// CheckDoc 校验一条待写入的文档：分区字段必须等于 part，正文不超过上限。
// 返回编码后的正文，两个实现直接存它，保证读回一致。
func CheckDoc(spec CollectionSpec, part string, doc Doc) ([]byte, error) {
	if doc.Key == "" {
		return nil, fmt.Errorf("store: 集合文档缺 key")
	}
	if doc.Body == nil {
		return nil, fmt.Errorf("store: 集合文档 %s 缺正文", doc.Key)
	}
	if fmt.Sprint(doc.Body[spec.Partition]) != part {
		return nil, fmt.Errorf("store: 集合文档 %s 的 %s 字段 %v 与分区 %q 不符", doc.Key, spec.Partition, doc.Body[spec.Partition], part)
	}
	for _, ix := range spec.Indexes {
		if _, ok := doc.Body[ix]; !ok {
			return nil, fmt.Errorf("store: 集合文档 %s 缺索引字段 %s", doc.Key, ix)
		}
	}
	b, err := json.Marshal(doc.Body)
	if err != nil {
		return nil, fmt.Errorf("store: 集合文档 %s 正文不能编码：%w", doc.Key, err)
	}
	if len(b) > MaxCollectionBody {
		return nil, fmt.Errorf("store: 集合文档 %s 正文 %d 字节，超过 %d", doc.Key, len(b), MaxCollectionBody)
	}
	return b, nil
}
