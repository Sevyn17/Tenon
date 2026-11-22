package assemble

// 内核定义的插槽、服务类型与插槽默认实现（原 builtin 包，docs/90 - 归档/00 - 总体架构.md 第六节）。
// 插件之间不互相 import：服务的类型放在内核，实现放在插件。

import (
	"fmt"
	"os"
	"strings"

	"github.com/Sevyn17/tenon/runtime/ext"
	"github.com/Sevyn17/tenon/runtime/modelhub"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// SlotSecrets 凭证解析插槽（models 组件消费；缺省 EnvSecrets，应用可覆盖）。
// 其余插槽（存储四个、模型目录、Auth、Coordinator）没有消费方，已删（2026-11-04 审查 P1-50）。
const SlotSecrets = ext.SlotSecrets

// SlotModelSources 模型来源插槽（models 组件可选消费，没有缺省实现）：见 ModelSources。
const SlotModelSources = ext.SlotModelSources

// 内核约定的服务键。
const (
	ServiceDB        = ext.ServiceDB        // DBHandle（产品的存储插件）
	ServiceModelhub  = ext.ServiceModelhub  // *Models（models）
	ServiceCompactor = ext.ServiceCompactor // 实现 agent.Compactor；可实现 Redactable（compact-basic）
)

// PointOnSessionDelete 删除会话时的清理回调（data-contract D2）：登记 store.Cleaner。
const PointOnSessionDelete = ext.PointOnSessionDelete

// DBHandle 存储句柄：Start 之前 DB() 返回 nil。
// 需要 SQL 的插件实现 plugin.Requirer 声明 Requires{SQL: true}，再从 service:sql 取 store.SQL（T1b-1b）。
type DBHandle interface {
	DB() store.Store
	Blobs() toolhub.Artifacts
}

// Models modelhub 服务：Hub + 默认模型（别名）。协议适配器插件在 Setup 里向 Hub 登记。
//
// Model 通常在 models 插件的 Setup 里定下；填了 slot:model_sources 时改到 models 的 Start 才定（那时所有组件
// 都 Setup 完了，网关等插件登记的模型也在目录里）。要用缺省模型的组件在自己的 Start 或运行期读，不要在 Setup 里抓住它。
type Models struct {
	Hub   *modelhub.Hub
	Model string
	// Secrets 凭证解析（可空）：界面列模型时据此标出哪些提供方的凭证不可用。
	Secrets Secrets
	// Origin 缺省模型由哪项设置决定（models.list 的 effective 用），取 Origin* 常量；"" = 应用直接给的（WithModels 等）。
	Origin string
	// OriginRef 补充说明：auto 时是模型来源给的理由（如「自动：已登录平台」）；config 时由应用宿主细分后填（文件路径、变量名）。
	OriginRef string
	// Notice 回落提示：显式指定的缺省模型解析不了、改用了别的时写明原因；空 = 没有回落。
	Notice string
}

// Models.Origin 的取值。
const (
	OriginConfig  = "config"  // models 插件配置写了 model 或 base_url（profile、命令行参数或环境变量）
	OriginAuto    = "auto"    // 没写，由 slot:model_sources 决定
	OriginBuiltin = "builtin" // 都没有，用内置缺省
)

// ModelSources 产品的模型来源（slot:model_sources）：插件层目录 + 用户没指定缺省模型时用谁。
// models 插件可选消费；不填时 models 的行为与没有这个插槽时一样。
type ModelSources interface {
	// Catalog 并入目录插件层（modelhub.LayerPlugin）的声明，如用户自己配的多套端点。models 在 Setup 里合入。
	Catalog() modelhub.CatalogFile
	// DefaultModel 用户没指定缺省模型时用谁；cat 是各组件都登记过之后的目录。返回 "" = 用内置缺省。
	// why 是给界面看的理由（如「自动：已登录平台」）。
	DefaultModel(cat *modelhub.Catalog) (model, why string)
}

// CredentialDescriber Secrets 的可选扩展：说明一个提供方的凭证从哪来（models.list 的 effective 与可用性用）。
// 不实现、或 ok = false 时按缺省规则：key 以 env: 开头是环境变量，空 key 是不需要凭证，其余按 Resolve 的结果。
type CredentialDescriber interface {
	// DescribeCredential 按提供方的协议与 key 引用说明凭证：kind 如 env / none 或产品自定的种类，label 给人看；
	// problem 非空 = 凭证现在不可用（原因）。
	DescribeCredential(protocol, key string) (kind, label, problem string, ok bool)
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

// SlotDefaults 插槽默认实现（未被插件填充时使用）。
func SlotDefaults() map[string]any {
	return map[string]any{
		SlotSecrets: Secrets(EnvSecrets{}),
	}
}
