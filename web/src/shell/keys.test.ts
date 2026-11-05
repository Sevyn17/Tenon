import { describe, expect, it } from "vitest";
import { conflicts, createDispatcher, matchChord, normalize, parseChord, type KeyLike } from "./keys";

const key = (k: string, m: Partial<KeyLike> = {}): KeyLike => ({ key: k, metaKey: false, ctrlKey: false, shiftKey: false, altKey: false, ...m });

describe("快捷键", () => {
  it("Mod 在 Mac 上是 ⌘，在其他平台是 Ctrl", () => {
    const c = parseChord("Mod+K")!;
    expect(matchChord(c, key("k", { metaKey: true }), true)).toBe(true);
    expect(matchChord(c, key("k", { ctrlKey: true }), true)).toBe(false);
    expect(matchChord(c, key("k", { ctrlKey: true }), false)).toBe(true);
    expect(matchChord(c, key("k", { ctrlKey: true, shiftKey: true }), false)).toBe(false);
  });

  it("Shift 改变字符的键（?）按字符匹配", () => {
    expect(matchChord(parseChord("?")!, key("?", { shiftKey: true }), false)).toBe(true);
  });

  it("规范化后相同的写法算冲突", () => {
    expect(normalize("Shift+Mod+k")).toBe(normalize("Mod+Shift+K"));
    const c = conflicts(new Map([["a", "Mod+Shift+K"], ["b", "shift+mod+k"], ["c", "Mod+K"], ["d", ""]]));
    expect([...c.values()]).toEqual([["a", "b"]]);
  });

  it("两段式快捷键：第一段命中后等第二段，超时作废", () => {
    const d = createDispatcher(false);
    const b = new Map([["stop", "Escape Escape"], ["close", "Mod+W"]]);
    expect(d.match(b, key("Escape"), 0)).toBe(null);
    expect(d.match(b, key("Escape"), 100)).toBe("stop");
    expect(d.match(b, key("Escape"), 200)).toBe(null);
    expect(d.match(b, key("Escape"), 2000)).toBe(null); // 距第一段超过 800ms：重新算第一段
    expect(d.match(b, key("w", { ctrlKey: true }), 2100)).toBe("close");
  });
});
