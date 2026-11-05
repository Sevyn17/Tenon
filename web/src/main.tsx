// 入口：挂载工作台。
// ?gallery：组件陈列页（开发与设计审阅用）；?gallery=chat：会话组件陈列。各入口按需加载。
import { StrictMode, type ComponentType } from "react";
import { createRoot } from "react-dom/client";

const q = new URLSearchParams(location.search);

async function load(): Promise<ComponentType> {
  if (q.get("gallery") === "chat") return (await import("./gallery-chat")).GalleryChat;
  if (q.has("gallery")) return (await import("./gallery")).Gallery;
  const { createShell } = await import("./shell");
  return createShell({
    brand: {
      greeting: "今天想做点什么？",
      examples: ["看看这个目录里有什么", "帮我写一个 README", "运行测试并修复失败的用例"],
    },
  }).Root;
}

void load().then((Root) =>
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <Root />
    </StrictMode>,
  ),
);
