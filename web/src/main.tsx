// 入口：挂载外壳。
// ?gallery：组件陈列页（开发与设计审阅用），否则渲染工作台。两者按需加载：旧界面的 styles.css 也定义了
// :root 变量，和 @ybase/ui 的令牌同时加载会互相覆盖，所以只在旧界面里引入它（旧界面删除时一并删掉）。
import { StrictMode, type ComponentType } from "react";
import { createRoot } from "react-dom/client";

const gallery = new URLSearchParams(location.search).has("gallery");

async function load(): Promise<ComponentType> {
  if (gallery) return (await import("./gallery")).Gallery;
  await import("./styles.css");
  return (await import("./App")).App;
}

void load().then((Root) =>
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <Root />
    </StrictMode>,
  ),
);
