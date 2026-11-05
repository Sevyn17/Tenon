// 组件陈列页（前端方案第四节）：地址带 ?gallery 时代替工作台渲染，深浅主题、密度可切换。
// 只用于开发与设计审阅，不是产品功能；它只依赖 @ybase/ui，顺便验证 L2 能独立使用。

import { useState } from "react";
import { Copy, GitBranch, Plus, RotateCcw, Search, Settings, Trash2 } from "lucide-react";
import {
  Badge,
  Button,
  Card,
  CodeBlock,
  Count,
  DeclarativeView,
  Dialog,
  DiffView,
  Dot,
  EmptyState,
  IconButton,
  Input,
  Kbd,
  KeyValue,
  Markdown,
  Menu,
  Progress,
  ProgressRing,
  SchemaForm,
  Segmented,
  Select,
  SettingsNote,
  SettingsSection,
  Skeleton,
  Spinner,
  Switch,
  Tabs,
  Textarea,
  ToastProvider,
  Tooltip,
  applyDensity,
  applyTheme,
  useToast,
  type Density,
  type FormValue,
  type ThemePref,
} from "./ui";
import s from "./gallery.module.css";

const MD = `## 标题二

正文段落，带 **粗体**、*斜体*、\`行内代码\` 与 [链接](https://example.com)。

- 列表项一
- 列表项二

| 列 | 值 |
|---|---|
| a | 1 |
| b | 2 |

\`\`\`go
func main() {
	fmt.Println("hi")
}
\`\`\`

> 引用一段话。`;

const DIFF = `@@ -1,3 +1,3 @@
 package main
-import "fmt"
+import "log"
 func main() {}`;

export function Gallery() {
  return (
    <ToastProvider>
      <GalleryBody />
    </ToastProvider>
  );
}

function GalleryBody() {
  const [theme, setTheme] = useState<ThemePref>("system");
  const [density, setDensity] = useState<Density>("comfortable");
  const [tab, setTab] = useState<"a" | "b">("a");
  const [on, setOn] = useState(true);
  const [dlg, setDlg] = useState(false);
  const [form, setForm] = useState<FormValue>({ threshold: 0.85, model: "deepseek-chat", enabled: true });
  const toast = useToast();

  return (
    <div className={s.page}>
      <header className={s.head}>
        <h1>@ybase/ui 组件陈列</h1>
        <Segmented
          value={theme}
          options={[
            { value: "system", label: "跟随系统" },
            { value: "light", label: "浅色" },
            { value: "dark", label: "深色" },
          ]}
          onChange={(v) => {
            setTheme(v);
            applyTheme(v);
          }}
        />
        <Segmented
          value={density}
          options={[
            { value: "comfortable", label: "宽松" },
            { value: "compact", label: "紧凑" },
          ]}
          onChange={(v) => {
            setDensity(v);
            applyDensity(v);
          }}
        />
      </header>

      <section className={s.grid}>
        <Card title="按钮">
          <div className={s.row}>
            <Button variant="primary">主要</Button>
            <Button>默认</Button>
            <Button variant="outline">描边</Button>
            <Button variant="ghost">幽灵</Button>
            <Button variant="danger">危险</Button>
            <Button disabled>禁用</Button>
          </div>
          <div className={s.row}>
            <Button size="sm" icon={<Plus size={14} />}>
              小号
            </Button>
            <Button size="lg">大号</Button>
            <Tooltip label="复制" keybind="Mod+C">
              <IconButton label="复制">
                <Copy size={16} />
              </IconButton>
            </Tooltip>
            <IconButton label="分叉">
              <GitBranch size={16} />
            </IconButton>
            <IconButton label="删除">
              <Trash2 size={16} />
            </IconButton>
          </div>
        </Card>

        <Card title="状态与标记">
          <div className={s.row}>
            <Badge>中性</Badge>
            <Badge tone="accent">强调</Badge>
            <Badge tone="success">成功</Badge>
            <Badge tone="warning">警告</Badge>
            <Badge tone="danger">危险</Badge>
            <Count n={3} />
          </div>
          <div className={s.row}>
            <Dot /> <Dot state="running" /> <Dot state="warning" /> <Dot state="success" /> <Dot state="danger" />
            <Spinner />
            <ProgressRing value={0.62} title="上下文 62%" />
            <Kbd bind="Mod+K" />
            <Kbd bind="Mod+Shift+T" />
          </div>
          <Progress value={0.4} />
        </Card>

        <Card title="输入">
          <Input placeholder="单行输入" />
          <Textarea rows={3} placeholder="多行输入" />
          <div className={s.row}>
            <Select defaultValue="b">
              <option value="a">选项 A</option>
              <option value="b">选项 B</option>
            </Select>
            <Switch checked={on} onChange={setOn} label="开关" />
          </div>
        </Card>

        <Card title="菜单、对话框、提示">
          <div className={s.row}>
            <Menu
              trigger={({ ref, toggle }) => (
                <Button ref={ref} icon={<Settings size={14} />} onClick={toggle}>
                  菜单
                </Button>
              )}
              groups={[
                {
                  id: "main",
                  items: [
                    { id: "copy", label: "复制", icon: <Copy size={14} />, keybind: "Mod+C", run: () => toast.show("已复制") },
                    { id: "fork", label: "从这里分叉", icon: <GitBranch size={14} /> },
                    { id: "retry", label: "重试", icon: <RotateCcw size={14} />, disabled: true, hint: "回合进行中" },
                  ],
                },
                { id: "danger", items: [{ id: "del", label: "删除会话", icon: <Trash2 size={14} />, danger: true }] },
              ]}
            />
            <Button onClick={() => setDlg(true)}>对话框</Button>
            <Button onClick={() => toast.show("回合完成", { tone: "success" })}>提示</Button>
          </div>
          <Dialog
            open={dlg}
            onClose={() => setDlg(false)}
            title="确认回退"
            actions={
              <>
                <Button onClick={() => setDlg(false)}>取消</Button>
                <Button variant="danger" onClick={() => setDlg(false)}>
                  回退
                </Button>
              </>
            }
          >
            将截掉第 3 回合及之后的 4 条消息，此操作不能撤销。
          </Dialog>
        </Card>

        <Card title="标签页与空态" collapsible>
          <Tabs
            value={tab}
            onChange={(v) => setTab(v)}
            items={[
              { value: "a", label: "改动" },
              { value: "b", label: "任务 2" },
            ]}
          />
          {tab === "a" ? (
            <EmptyState icon={<Search size={20} />} title="还没有改动">
              智能体写文件后会显示在这里。
            </EmptyState>
          ) : (
            <>
              <Skeleton w="70%" />
              <Skeleton w="40%" />
            </>
          )}
        </Card>

        <Card title="键值">
          <KeyValue
            rows={[
              ["模型", "deepseek-chat"],
              ["输入 tokens", "12,480"],
              ["缓存命中", "83%"],
            ]}
          />
        </Card>

        <Card title="Markdown" className={s.wide}>
          <Markdown text={MD} />
        </Card>

        <Card title="代码与 diff" className={s.wide}>
          <CodeBlock code={'go test ./...\nok  ybase/appapi 1.2s'} lang="bash" />
          <DiffView diff={DIFF} />
        </Card>

        <Card title="由 JSON Schema 生成的表单" className={s.wide}>
          <SettingsSection title="compact-basic" description="插件配置（L1，按 schema 自动生成）">
            <SchemaForm
              schema={{
                type: "object",
                properties: {
                  threshold: { type: "number", title: "触发阈值", description: "上下文占用超过它时压缩" },
                  model: { type: "string", title: "总结模型", enum: ["deepseek-chat", "gpt-4o-mini"] },
                  enabled: { type: "boolean", title: "启用" },
                },
              }}
              value={form}
              onChange={setForm}
            />
          </SettingsSection>
          <SettingsNote>当前版本不支持在界面保存插件配置：请编辑 profile.toml 后重启。</SettingsNote>
        </Card>

        <Card title="声明式视图（后端插件贡献）" className={s.wide}>
          <DeclarativeView
            node={{
              type: "stack",
              children: [
                {
                  type: "row",
                  children: [
                    { type: "text", text: "部署检查", bold: true },
                    { type: "badge", text: "3 / 5", tone: "accent" },
                  ],
                },
                { type: "progress", value: 0.6, label: "进度" },
                {
                  type: "table",
                  columns: ["步骤", "状态"],
                  rows: [
                    ["构建", "完成"],
                    ["测试", "进行中"],
                  ],
                },
                { type: "button", label: "填入输入框", action: { kind: "insert", text: "继续部署" } },
              ],
            }}
            onAction={(a) => toast.show("动作：" + a.kind)}
          />
        </Card>
      </section>
    </div>
  );
}
