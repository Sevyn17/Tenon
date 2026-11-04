package assemble

// 内核定义的插槽、服务类型与插槽默认实现（原 builtin 包，docs/90 - 归档/00 - 总体架构.md 第六节）。
// 插件之间不互相 import：服务的类型放在内核，实现放在插件。

import (
	"fmt"
	"os"
	"strings"

	"ybase/runtime/modelhub"
	"ybase/runtime/store"
	"ybase/runtime/toolhub"
)

// 插槽键。
const (
	SlotSessionStore    = "slot:session_store"
	SlotToolResultStore = "slot:tool_result_store"
	SlotBlobStore       = "slot:blob_store"
	SlotAgentStore      = "slot:agent_store"
	SlotModelCatalog    = "slot:model_catalog"
	SlotSecrets         = "slot:secrets"
	SlotAuth            = "slot:auth"
	SlotCoordinator     = "slot:coordinator"
)

// 内核约定的服务键。
const (
	ServiceDB        = "service:db"        // DBHandle（store-sqlite）
	ServiceModelhub  = "service:modelhub"  // *Models（models）
	ServiceCompactor = "service:compactor" // 实现 agent.Compactor；可实现 Redactable（compact-basic）
)

// PointOnSessionDelete 删除会话时的清理回调（data-contract D2）：登记 store.Cleaner。
const PointOnSessionDelete = "on_session_delete"

// DBHandle 存储句柄：Start 之前 DB() 返回 nil。
// 需要 SQL 的插件实现 plugin.Requirer 声明 Requires{SQL: true}，再从 service:sql 取 store.SQL（T1b-1b）。
type DBHandle interface {
	DB() store.Store
	Blobs() toolhub.Artifacts
}

// Models modelhub 服务：Hub + 默认模型（别名）。协议适配器插件在 Setup 里向 Hub 登记。
type Models struct {
	Hub   *modelhub.Hub
	Model string
}

// Redactable 可装脱敏函数的服务（如压缩器：摘要落库前脱敏）。
type Redactable interface {
	SetRedact(func(string) string)
}

// Secrets 凭证解析：只接受引用（model-hub D4），不存明文。
type Secrets interface {
	Resolve(ref string) (string, error)
}

// EnvSecrets 默认实现：`env:NAME`。
type EnvSecrets struct{}

// Resolve 解析引用。
func (EnvSecrets) Resolve(ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	name, ok := strings.CutPrefix(ref, "env:")
	if !ok {
		return "", fmt.Errorf("secrets: 不支持的引用 %q（默认实现只认 env:）", ref)
	}
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("secrets: 环境变量 %s 未设置", name)
	}
	return v, nil
}

// ModelCatalog 别名 → 模型名。
type ModelCatalog interface {
	Resolve(alias string) string
}

// IdentityCatalog 默认实现：别名即模型名。
type IdentityCatalog struct{}

// Resolve 原样返回。
func (IdentityCatalog) Resolve(alias string) string { return alias }

// NopAuth 本地单用户（review-1 R7：只留接口）。
type NopAuth struct{}

// NopCoordinator 单进程（review-1 R7）。
type NopCoordinator struct{}

// SlotDefaults 插槽默认实现（未被插件填充时使用）。
func SlotDefaults() map[string]any {
	return map[string]any{
		SlotModelCatalog: IdentityCatalog{},
		SlotSecrets:      Secrets(EnvSecrets{}),
		SlotAuth:         NopAuth{},
		SlotCoordinator:  NopCoordinator{},
	}
}
