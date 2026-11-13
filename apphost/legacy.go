package apphost

// 旧版会话库并入用户级库：以前每个项目一个库（<项目>/.tenon/state.db + blobs/），现在一个用户一个库。
// 打开一个工作区时，项目里还有旧库就把它的会话并进来、记上这个工作区，再把旧库改名成
// state.db.imported（不删：要回退时改回原名即可）。附件目录只复制不动。
//
// 并入本身是存储实现的事：框架只编排（找旧库、判断是不是当前库、改名、出提示），
// 真正读旧库写新库由存储插件实现 LegacyImporter。没有实现这个接口的存储插件会跳过，不报错。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Sevyn17/tenon/runtime/appid"
)

// LegacyImportedSuffix 并入后旧库文件的新后缀。
const LegacyImportedSuffix = ".imported"

// LegacyImportResult 并库结果，由存储插件的 LegacyImporter 返回。
type LegacyImportResult struct {
	Sessions int      // 导入的会话数（含子会话）
	Skipped  []string // 本库已有、没导入的会话 id
}

// LegacyImporter 存储插件可选实现的旧库并入能力。
// 存储插件的 DB() 返回值实现它，框架在打开工作区时用类型断言调用；没实现就跳过。
type LegacyImporter interface {
	// Import 把 path 处的旧库并进本库；源库里没记工作区的会话记成 workspace。
	Import(ctx context.Context, path, workspace string) (LegacyImportResult, error)
	// Path 当前库的文件路径，用来判断旧库是不是就是当前库。
	Path() string
}

type blobImporter interface {
	ImportDir(src string) (int, error)
}

// importLegacy 并入 root/.tenon 下的旧库；没有旧库、存储插件不支持并入、
// 或者旧库就是当前库时什么也不做。返回给用户看的提示；出错也只提示，不挡启动。
func (r *Runtime) importLegacy(ctx context.Context) []string {
	if r.Rt == nil || r.Rt.Store == nil {
		return nil
	}
	dir := appid.Project(r.Root)
	old := filepath.Join(dir, "state.db")
	st, err := os.Stat(old)
	if err != nil || st.IsDir() {
		return nil
	}
	db, ok := r.Rt.Store.DB().(LegacyImporter)
	if !ok {
		return nil // 存储插件不支持并入旧库，不动
	}
	if cur, err := os.Stat(db.Path()); err == nil && os.SameFile(cur, st) {
		return nil
	}
	res, err := db.Import(ctx, old, WorkspaceKey(r.Root))
	if err != nil {
		return []string{fmt.Sprintf("旧会话库 %s 没能并入用户级会话库：%v（原样保留，下次启动再试）", old, err)}
	}
	var notes []string
	if b, ok := r.Rt.Store.Blobs().(blobImporter); ok {
		if _, err := os.Stat(filepath.Join(dir, "blobs")); err == nil {
			if _, err := b.ImportDir(filepath.Join(dir, "blobs")); err != nil {
				notes = append(notes, fmt.Sprintf("旧附件目录没能全部复制：%v", err))
			}
		}
	}
	for _, sfx := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(old + sfx); err == nil {
			if err := os.Rename(old+sfx, old+sfx+LegacyImportedSuffix); err != nil {
				notes = append(notes, fmt.Sprintf("旧会话库改名失败（下次启动会再并一次，已有的会话不会重复）：%v", err))
				break
			}
		}
	}
	msg := fmt.Sprintf("已把这个项目的 %d 个旧会话并入用户级会话库（旧库改名为 %s）", res.Sessions, filepath.Base(old)+LegacyImportedSuffix)
	if len(res.Skipped) > 0 {
		msg += fmt.Sprintf("；%d 个会话 id 已存在，没有导入", len(res.Skipped))
	}
	return append([]string{msg}, notes...)
}
