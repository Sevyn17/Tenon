// 入口：挂载工作台。
// ?gallery：组件陈列页（开发与设计审阅用）；?gallery=chat：会话组件陈列；?legacy：旧界面（第 8 步删除）。
// 各入口按需加载：旧界面的 styles.css 也定义了 :root 变量，和 @ybase/ui 的令牌同时加载会互相覆盖，所以只在旧界面里引入它。
import { StrictMode, type ComponentType } from "react";
import { createRoot } from "react-dom/client";

const q = new URLSearchParams(location.search);

async function load(): Promise<ComponentType> {
  if (q.get("gallery") === "chat") return (await import("./gallery-chat")).GalleryChat;
  if (q.has("gallery")) return (await import("./gallery")).Gallery;
  if (q.has("legacy")) {
    await import("./styles.css");
    return (await import("./App")).App;
  }
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
