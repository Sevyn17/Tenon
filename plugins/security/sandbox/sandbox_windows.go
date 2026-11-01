//go:build windows

package sandbox

// Windows 沙箱：写受限令牌（WRITE_RESTRICTED）+ 工作区能力 SID，不改完整性级别。
//
// 写访问要通过两轮检查：普通 SID 一轮（同用户本身）+ 受限 SID 一轮（能力 SID、登录 SID、Everyone）。
// 工作区 DACL 追加一条可继承的“能力 SID 可写”ACE，受保护目录（.ybase）追加能力 SID 拒写 ACE；
// 于是 workspace-write 只能写工作区（及专用临时目录），read-only 不带能力 SID，哪里都写不了
// （Everyone 可写的目录除外，已知缺口）。读不受影响（WRITE_RESTRICTED 只限制写）。
// 与旧方案（Low 完整性 + 给工作区打 Low 标签）相比：工作区不再被降级，工作区里的程序照常运行。

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	disableMaxPrivilege = 0x1
	luaToken            = 0x4
	writeRestricted     = 0x8

	fileDeleteChild = 0x40

	// 能力 SID 在工作区上的权限：读写执行 + 删除（不含改 DACL / 改属主）。
	capAccess = windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE | windows.FILE_GENERIC_EXECUTE | windows.DELETE | fileDeleteChild
	// 受保护目录对能力 SID 拒绝的写类权限（不含 SYNCHRONIZE / READ_CONTROL，否则读也坏）。
	denyWrite = windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA | windows.FILE_WRITE_ATTRIBUTES |
		windows.DELETE | fileDeleteChild | windows.WRITE_DAC | windows.WRITE_OWNER

	labelLow    = "S:(ML;OICI;NW;;;LW)" // 旧方案遗留标签（Reset 时清除）
	inheritBoth = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
)

var (
	advapi32                  = windows.NewLazySystemDLL("advapi32.dll")
	procCreateRestrictedToken = advapi32.NewProc("CreateRestrictedToken")

	granted sync.Map // root → error（每进程每工作区只授一次）
)

func apply(s *Sandbox, cmd *exec.Cmd, mode Mode) (func(), error) {
	root, err := filepath.Abs(s.Root)
	if err != nil {
		return nil, fmt.Errorf("%w：%v", ErrUnavailable, err)
	}
	cap, err := CapabilitySID(root)
	if err != nil {
		return nil, fmt.Errorf("%w：%v", ErrUnavailable, err)
	}
	var tmp string
	if mode == WorkspaceWrite {
		if tmp, err = grantWorkspace(s, root, cap); err != nil {
			return nil, fmt.Errorf("%w：工作区授权失败：%v", ErrUnavailable, err)
		}
	}
	tok, err := restrictedToken(cap, mode == WorkspaceWrite)
	if err != nil {
		return nil, fmt.Errorf("%w：%v", ErrUnavailable, err)
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Token = syscall.Token(tok)
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	if tmp != "" { // 用户 TEMP 对受限令牌不可写：换成能力 SID 可写的专用目录
		env = setEnv(env, "TEMP", tmp)
		env = setEnv(env, "TMP", tmp)
		// 用户级构建缓存（%LOCALAPPDATA%\go-build 等）对受限令牌不可写，go build / test 会失败；
		// 指到专用目录下（按工作区持久，跨调用复用）。不授权用户缓存本身：避免沙箱进程投毒共享缓存。
		if !s.KeepGoCache {
			gc := filepath.Join(tmp, "go-build")
			if os.MkdirAll(gc, 0o755) == nil {
				env = setEnv(env, "GOCACHE", gc)
			}
		}
	}
	cmd.Env = env
	return func() { tok.Close() }, nil
}

// CapabilitySID 由工作区规范路径派生的能力 SID（确定性：重启后不变，无需另存）。
func CapabilitySID(root string) (*windows.SID, error) {
	h := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(root))))
	return windows.StringToSid(fmt.Sprintf("S-1-5-21-%d-%d-%d-%d",
		binary.LittleEndian.Uint32(h[0:]), binary.LittleEndian.Uint32(h[4:]),
		binary.LittleEndian.Uint32(h[8:]), binary.LittleEndian.Uint32(h[12:])))
}

// restrictedToken 当前进程令牌的写受限副本。受限 SID：[能力 SID]、登录 SID、Everyone。
// 登录 SID 让进程仍能写自己的窗口站 / 桌面（缺了会 0xC0000142）。
func restrictedToken(cap *windows.SID, withCap bool) (windows.Token, error) {
	var cur windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(),
		windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY|windows.TOKEN_ADJUST_DEFAULT|windows.TOKEN_ASSIGN_PRIMARY, &cur); err != nil {
		return 0, fmt.Errorf("打开进程令牌：%v", err)
	}
	defer cur.Close()
	logon, err := logonSID(cur)
	if err != nil {
		return 0, err
	}
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		return 0, err
	}
	ownerRights, err := windows.StringToSid("S-1-3-4")
	if err != nil {
		return 0, err
	}
	var sids []windows.SIDAndAttributes
	if withCap {
		sids = append(sids, windows.SIDAndAttributes{Sid: cap})
	}
	sids = append(sids, windows.SIDAndAttributes{Sid: logon}, windows.SIDAndAttributes{Sid: everyone})
	var pin runtime.Pinner
	defer pin.Unpin()
	for _, s := range sids {
		pin.Pin(s.Sid)
	}
	var tok windows.Token
	r, _, e := procCreateRestrictedToken.Call(uintptr(cur), disableMaxPrivilege|luaToken|writeRestricted,
		0, 0, 0, 0, uintptr(len(sids)), uintptr(unsafe.Pointer(&sids[0])), uintptr(unsafe.Pointer(&tok)))
	if r == 0 {
		return 0, fmt.Errorf("创建受限令牌：%v", e)
	}
	// 缺省 DACL：进程自己创建的内核对象（线程、管道等）要让受限 SID 也能写
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		grant(logon, windows.GENERIC_ALL, windows.NO_INHERITANCE),
		grant(ownerRights, windows.READ_CONTROL, windows.NO_INHERITANCE),
	}, nil)
	if err == nil {
		dd := struct{ DefaultDacl *windows.ACL }{acl}
		err = windows.SetTokenInformation(tok, windows.TokenDefaultDacl, (*byte)(unsafe.Pointer(&dd)), uint32(unsafe.Sizeof(dd)))
	}
	if err != nil {
		tok.Close()
		return 0, fmt.Errorf("设置缺省 DACL：%v", err)
	}
	return tok, nil
}

func logonSID(t windows.Token) (*windows.SID, error) {
	gs, err := t.GetTokenGroups()
	if err != nil {
		return nil, fmt.Errorf("读取令牌组：%v", err)
	}
	for _, g := range gs.AllGroups() {
		if g.Attributes&windows.SE_GROUP_LOGON_ID == windows.SE_GROUP_LOGON_ID {
			return g.Sid.Copy()
		}
	}
	return nil, fmt.Errorf("令牌没有登录 SID")
}

func grant(sid *windows.SID, mask windows.ACCESS_MASK, inherit uint32) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: mask,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inherit,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
}

// editDACL 在 path 现有 DACL 上合并 entries（保留继承，不动其他 ACE）。
func editDACL(path string, entries []windows.EXPLICIT_ACCESS) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	old, _, err := sd.DACL()
	if err != nil {
		return err
	}
	acl, err := windows.ACLFromEntries(entries, old)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

// grantWorkspace 工作区根授能力 SID 可写（可继承），受保护目录拒写；返回专用临时目录。
func grantWorkspace(s *Sandbox, root string, cap *windows.SID) (string, error) {
	tmp, err := sandboxTemp(s.TempDir, root)
	if err != nil {
		return "", err
	}
	if v, ok := granted.Load(root); ok {
		if v == nil {
			return tmp, nil
		}
		return "", v.(error)
	}
	err = func() error {
		var pin runtime.Pinner
		pin.Pin(cap)
		defer pin.Unpin()
		if err := editDACL(root, []windows.EXPLICIT_ACCESS{grant(cap, capAccess, inheritBoth)}); err != nil {
			return err
		}
		if err := editDACL(tmp, []windows.EXPLICIT_ACCESS{grant(cap, capAccess, inheritBoth)}); err != nil {
			return err
		}
		for _, p := range s.protect() {
			d := filepath.Join(root, p)
			if err := os.MkdirAll(d, 0o755); err != nil {
				return err
			}
			deny := grant(cap, denyWrite, inheritBoth)
			deny.AccessMode = windows.DENY_ACCESS
			if err := editDACL(d, []windows.EXPLICIT_ACCESS{deny}); err != nil {
				return err
			}
		}
		return nil
	}()
	if err != nil {
		granted.Store(root, err)
		return "", err
	}
	granted.Store(root, nil)
	return tmp, nil
}

// sandboxTemp <dir>\<工作区哈希>；dir 空 = %LOCALAPPDATA%\ybase\sbtmp。
func sandboxTemp(dir, root string) (string, error) {
	if dir == "" {
		base, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0)
		if err != nil {
			return "", err
		}
		dir = filepath.Join(base, "ybase", "sbtmp")
	}
	h := sha256.Sum256([]byte(strings.ToLower(root)))
	d := filepath.Join(dir, hex.EncodeToString(h[:6]))
	return d, os.MkdirAll(d, 0o755)
}

// Reset 撤销沙箱对工作区做的全部修改：能力 SID 的 ACE（根、受保护目录、临时目录），
// 以及旧版本（Low 完整性方案）留下的 Low / Medium 标签。
func Reset(root string, protect []string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	cap, err := CapabilitySID(root)
	if err != nil {
		return err
	}
	var pin runtime.Pinner
	pin.Pin(cap)
	defer pin.Unpin()
	revoke := grant(cap, 0, windows.NO_INHERITANCE)
	revoke.AccessMode = windows.REVOKE_ACCESS
	if protect == nil {
		protect = []string{".ybase"}
	}
	paths := []string{root}
	for _, p := range protect {
		paths = append(paths, filepath.Join(root, p))
	}
	if tmp, err := sandboxTemp("", root); err == nil { // 只撤缺省位置；自定义 temp_dir 的由用户自行删除
		paths = append(paths, tmp)
	}
	var errs []string
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if err := editDACL(p, []windows.EXPLICIT_ACCESS{revoke}); err != nil {
			errs = append(errs, fmt.Sprintf("%s：%v", p, err))
		}
		if err := clearLabel(p); err != nil {
			errs = append(errs, fmt.Sprintf("%s 标签：%v", p, err))
		}
	}
	granted.Delete(root)
	if len(errs) > 0 {
		return fmt.Errorf("sandbox: 恢复不完整：%s", strings.Join(errs, "；"))
	}
	return nil
}

// clearLabel 去掉显式完整性标签（写空 SACL 标签部分），继承的标签随父目录一起消失。
func clearLabel(path string) error {
	sd, err := windows.SecurityDescriptorFromString("S:")
	if err != nil {
		return err
	}
	sacl, _, err := sd.SACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.LABEL_SECURITY_INFORMATION, nil, nil, nil, sacl)
}

func setEnv(env []string, k, v string) []string {
	p := strings.ToUpper(k) + "="
	out := env[:0:0]
	for _, e := range env {
		if !strings.HasPrefix(strings.ToUpper(e), p) {
			out = append(out, e)
		}
	}
	return append(out, k+"="+v)
}
