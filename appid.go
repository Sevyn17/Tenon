package tenon

import "github.com/Sevyn17/tenon/runtime/appid"

// AppID 应用身份：程序名、数据目录名、环境变量前缀。零值字段取缺省（tenon / .tenon / TENON）。
//
// 框架按它取用户级目录（$<Env>_HOME，缺省 ~/<Dir>）、项目目录（<工作目录>/<Dir>）、
// 环境变量名（<Env>_API_KEY 等）和提示里的命令名（"<Name> trust"）。
// 插件包清单 tenon-plugin.toml、${TENON_PLUGIN_DIR}、TENON_HOOK_EVENT / TENON_SESSION_ID 不随它变：
// 那是框架和第三方插件、脚本之间的协议。
type AppID = appid.ID

// SetAppID 设置本进程的应用身份。进程级设置，在 New / apphost.Start 之前调一次。
func SetAppID(id AppID) { appid.Set(id) }

// CurrentAppID 当前应用身份（没设过时为缺省）。
func CurrentAppID() AppID { return appid.Get() }
