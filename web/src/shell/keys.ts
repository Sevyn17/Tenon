// 快捷键（前端方案第十三节）：快捷键是命令的字段，没有单独的快捷键注册表。
//
// 写法："Mod+Shift+K"（Mod = Mac 上 ⌘、其他平台 Ctrl）；连按两次用空格分隔："Escape Escape"。
// 输入框聚焦时快捷键让路（命令 global 为真的除外）；有模态框时只响应模态框内的键（由调用方按焦点判断）。
// 用户改绑存在偏好里（prefs.keybinds：命令 id → 写法，空串 = 解绑）。

export interface Chord {
  mod: boolean;
  ctrl: boolean;
  shift: boolean;
  alt: boolean;
  key: string; // 小写；特殊键用 KeyboardEvent.key 的小写形式（escape、enter、arrowup、\\ …）
}

const ALIASES: Record<string, string> = { esc: "escape", backslash: "\\", slash: "/", comma: ",", period: ".", space: " " };

export function parseChord(s: string): Chord | null {
  const parts = s.split("+").map((p) => p.trim()).filter(Boolean);
  if (!parts.length) return null;
  const c: Chord = { mod: false, ctrl: false, shift: false, alt: false, key: "" };
  for (const raw of parts) {
    const p = raw.toLowerCase();
    if (p === "mod" || p === "cmd" || p === "meta") c.mod = true;
    else if (p === "ctrl" || p === "control") c.ctrl = true;
    else if (p === "shift") c.shift = true;
    else if (p === "alt" || p === "option") c.alt = true;
    else c.key = ALIASES[p] ?? p;
  }
  return c.key ? c : null;
}

/** 一个快捷键：一到两个和弦。 */
export function parseBinding(s: string): Chord[] {
  return s
    .split(/\s+/)
    .map(parseChord)
    .filter((c): c is Chord => c !== null);
}

/** 规范化写法（冲突比较用）。 */
export function normalize(s: string): string {
  return parseBinding(s)
    .map((c) => [c.mod && "mod", c.ctrl && "ctrl", c.alt && "alt", c.shift && "shift", c.key].filter(Boolean).join("+"))
    .join(" ");
}

export interface KeyLike {
  key: string;
  metaKey: boolean;
  ctrlKey: boolean;
  shiftKey: boolean;
  altKey: boolean;
}

/** 按键是否匹配和弦。mac 上 Mod = ⌘，否则 Mod = Ctrl（此时单独写的 Ctrl 与 Mod 等价）。 */
export function matchChord(c: Chord, e: KeyLike, mac: boolean): boolean {
  let key = e.key.toLowerCase();
  if (key === "esc") key = "escape";
  // Shift 改变了字符（? / ! 等）时，按字符匹配、不再要求 shift 修饰
  const shiftedChar = key.length === 1 && !/[a-z0-9]/.test(key);
  const wantMeta = mac ? c.mod : false;
  const wantCtrl = mac ? c.ctrl : c.mod || c.ctrl;
  if (e.metaKey !== wantMeta) return false;
  if (e.ctrlKey !== wantCtrl) return false;
  if (e.altKey !== c.alt) return false;
  if (!shiftedChar && e.shiftKey !== c.shift) return false;
  return key === c.key;
}

/** 快捷键冲突：规范化后相同的写法 → 用到它的命令 id。 */
export function conflicts(bindings: Map<string, string>): Map<string, string[]> {
  const by = new Map<string, string[]>();
  for (const [id, b] of bindings) {
    if (!b) continue;
    const n = normalize(b);
    by.set(n, [...(by.get(n) ?? []), id]);
  }
  for (const [n, ids] of [...by]) if (ids.length < 2) by.delete(n);
  return by;
}

/**
 * 按键分发器：给它当前全部"命令 id → 写法"，喂 keydown，返回命中的命令 id。
 * 两段式快捷键的第一段命中后等 800ms 内的第二段。
 */
export function createDispatcher(mac: boolean, chordTimeout = 800) {
  let pending: { ids: Set<string>; until: number } | null = null;
  return {
    match(bindings: Map<string, string>, e: KeyLike, now = Date.now()): string | null {
      const parsed = [...bindings].map(([id, b]) => [id, parseBinding(b)] as const).filter(([, cs]) => cs.length);
      if (pending && now <= pending.until) {
        const p = pending;
        pending = null;
        for (const [id, cs] of parsed) if (p.ids.has(id) && cs.length === 2 && matchChord(cs[1], e, mac)) return id;
      }
      pending = null;
      const firsts = new Set<string>();
      for (const [id, cs] of parsed) {
        if (!matchChord(cs[0], e, mac)) continue;
        if (cs.length === 1) return id;
        firsts.add(id);
      }
      if (firsts.size) pending = { ids: firsts, until: now + chordTimeout };
      return null;
    },
    reset() {
      pending = null;
    },
  };
}
