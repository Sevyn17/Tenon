// 前端分层的导入方向检查（同 Go 的 runtime/assemble/deps_test.go）：
//   client/        L0 协议客户端：不得 import react、ui/、shell/、client/react/
//   client/react/  L1 hooks：只依赖 client/ 与 react
//   ui/            L2 设计系统与会话组件：不得 import client/、shell/
//   shell/         L3 工作台：不受限制
// 违规时列出文件与导入并以非零退出。挂在 pnpm typecheck 前面。

import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative, resolve, dirname, sep } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..", "src");
const files = [];
(function walk(d) {
  for (const n of readdirSync(d)) {
    const p = join(d, n);
    if (statSync(p).isDirectory()) walk(p);
    else if (/\.(ts|tsx)$/.test(n)) files.push(p);
  }
})(root);

const layer = (p) => {
  const r = relative(root, p).split(sep).join("/");
  if (r.startsWith("client/react/")) return "react-hooks";
  if (r.startsWith("client/")) return "client";
  if (r.startsWith("ui/")) return "ui";
  if (r.startsWith("shell/")) return "shell";
  return "app";
};

const rules = {
  client: (to, spec) => to === "client" && !/^react(-dom)?(\/|$)/.test(spec),
  "react-hooks": (to) => to === "client" || to === "react-hooks" || to === "pkg",
  ui: (to) => to === "ui" || to === "pkg",
  shell: () => true,
  app: () => true,
};

const bad = [];
const re = /(?:import|export)\s[^'"]*?from\s*["']([^"']+)["']|import\(\s*["']([^"']+)["']\s*\)|import\s+["']([^"']+)["']/g;
for (const f of files) {
  const from = layer(f);
  const src = readFileSync(f, "utf8");
  for (const m of src.matchAll(re)) {
    const spec = m[1] ?? m[2] ?? m[3];
    let to;
    if (spec.startsWith(".")) to = layer(resolve(dirname(f), spec));
    else to = from === "client" && /^react(-dom)?(\/|$)/.test(spec) ? "react" : "pkg";
    const ok = from === "client" ? to === "client" || (to === "pkg" && !/^react/.test(spec)) : rules[from](to, spec);
    if (!ok) bad.push(`${relative(root, f)}: ${from} 不得 import ${spec}（${to}）`);
  }
}
if (bad.length) {
  console.error("前端分层导入方向违规：\n  " + bad.join("\n  "));
  process.exit(1);
}
console.log(`check-deps: ${files.length} 个文件，导入方向正确`);
