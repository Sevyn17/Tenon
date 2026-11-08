// 框架的缺省入口：挂载工作台（SDK 示例、没有自己前端的应用嵌的就是它）。
// ?gallery：组件陈列页（开发与设计审阅用）；?gallery=chat：会话组件陈列。各入口按需加载。
import { StrictMode, type ComponentType } from "react";
import { createRoot } from "react-dom/client";

const q = new URLSearchParams(location.search);

async function load(): Promise<ComponentType> {
  if (q.get("gallery") === "chat") return (await import("./gallery-chat")).GalleryChat;
  if (q.has("gallery")) return (await import("./gallery")).Gallery;
  const { createShell } = await import("./shell");
  // 框架的缺省入口：中性品牌，不带产品的示例提示。产品有自己的入口（如 client/web），引用 @tenon/web 再 createShell。
  return createShell().Root;
}

void load().then((Root) =>
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <Root />
    </StrictMode>,
  ),
);
