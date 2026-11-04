package main

// ybase plugin 子命令（插件包，见 docs/90 - 归档/10 - 框架模块/15 - 插件包.md）：
//
//	ybase plugin list                 列出已加载的插件包（用户级、受信时的项目级）
//	ybase plugin new <名字> [-project] 在 ~/.ybase/plugins/<名字>（或 <dir>/.ybase/plugins/<名字>）生成骨架

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"ybase/apps/internal/boot"
	"ybase/runtime/pkgs"
)

func pluginCmd(args []string, o opts) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list":
		root, err := filepath.Abs(o.workdir)
		if err != nil {
			return err
		}
		trusted, why, err := resolveTrust(o, root, false)
		if err != nil {
			return err
		}
		_, ps, notes, err := boot.LoadAll(o.profile, root, trusted)
		if err != nil {
			return err
		}
		fmt.Printf("项目信任：%v（%s）\n", trusted, why)
		for _, n := range notes {
			fmt.Println("注意：" + n)
		}
		for _, l := range packageLines(ps) {
			fmt.Println(l)
		}
		return nil
	case "new":
		var name string
		project := false
		for _, a := range args[1:] {
			switch a {
			case "-project", "--project":
				project = true
			default:
				name = a
			}
		}
		if name == "" {
			return errors.New("用法：ybase plugin new <名字> [-project]")
		}
		base := boot.Home()
		if project {
			root, err := filepath.Abs(o.workdir)
			if err != nil {
				return err
			}
			base = filepath.Join(root, ".ybase")
		}
		if base == "" {
			return errors.New("取不到用户目录（可设 YBASE_HOME）")
		}
		dir, err := pkgs.Scaffold(filepath.Join(base, "plugins"), name)
		if err != nil {
			return err
		}
		fmt.Printf("已生成插件包 %s\n编辑 %s 后重启生效（ybase plugin list 查看）\n", dir, filepath.Join(dir, pkgs.ManifestName))
		return nil
	}
	fmt.Fprintln(os.Stderr, "用法：ybase plugin list | new <名字> [-project]")
	return fmt.Errorf("未知子命令 %q", args[0])
}
