package main

// 权限档位的查看与复制：/mode、/mode copy 与子命令 ybase mode list|copy。

import (
	"fmt"
	"path/filepath"

	"ybase/modules/security/permission"
)

// printModes 列出全部档位（内置在前，自定义按名字）。
func printModes(reg *permission.Registry) {
	fmt.Println("可选档位：")
	for _, d := range reg.List() {
		src := "内置"
		if !d.Builtin {
			src = d.Source
		}
		title := d.Title
		if title == "" {
			title = string(d.ID)
		}
		line := fmt.Sprintf("  %-12s %s", d.ID, title)
		if d.Description != "" {
			line += "：" + d.Description
		}
		fmt.Printf("%s（%s）\n", line, src)
	}
}

// modeCopy /mode copy <源> <新名> [-project]：缺省写到用户级目录，-project 写到项目 .ybase/modes。
func modeCopy(reg *permission.Registry, args []string) error {
	var pos []string
	project := false
	for _, a := range args {
		if a == "-project" || a == "--project" {
			project = true
		} else {
			pos = append(pos, a)
		}
	}
	if len(pos) != 2 {
		return fmt.Errorf("用法：copy <源档位> <新档位名> [-project]")
	}
	dirs := reg.Dirs
	var dir string
	switch {
	case project && len(dirs) > 1:
		dir = dirs[len(dirs)-1]
	case project:
		return fmt.Errorf("项目不受信（或没有项目目录），不能写项目级档位")
	case len(dirs) > 0:
		dir = dirs[0]
	}
	p, err := reg.Copy(permission.Mode(pos[0]), permission.Mode(pos[1]), dir)
	if err != nil {
		return err
	}
	fmt.Printf("已创建档位 %s：%s\n编辑该文件后，下一回合自动生效；/mode %s 切换\n", pos[1], p, pos[1])
	return nil
}

// modeCmd 子命令 ybase mode list | copy <源> <新名> [-project]。
func modeCmd(args []string, workdir string) error {
	root, err := filepath.Abs(workdir)
	if err != nil {
		return err
	}
	reg, err := permission.NewRegistry(permission.ModeDirs(root)...)
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "list" {
		printModes(reg)
		return nil
	}
	if args[0] == "copy" {
		return modeCopy(reg, args[1:])
	}
	return fmt.Errorf("用法：ybase mode list | copy <源> <新名> [-project]")
}
