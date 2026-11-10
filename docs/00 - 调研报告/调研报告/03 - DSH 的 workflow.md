# DSH 的 workflow

## 一句话定位

DSH 的 workflow **不是拖拽式 DAG 工作流**，而是“**模型写一段 JavaScript，在沙箱进程里把活扇出给几十个子代理，只把最终 JSON 拿回来**”的代码化编排。

类比：标准模式像你站在车间里，一张一张派工单、等回执、再派下一张；workflow 像你写了一段调度程序交给传送带，它自己跑完，最后只把一张汇总质检单放回你桌上。

---

## 二、它是四个包拼出来的

| 包                           | 角色                                                         | 关键设计                                 |
| ---------------------------- | ------------------------------------------------------------ | ---------------------------------------- |
| `dsh-workflow`               | **契约层**（seam）：定义 `ctx.workflowEngine`、运行/结果/事件词汇 | 只定义行为，不含执行；换引擎不改可见行为 |
| `dsh-tool-workflow`          | **模型侧工具**：`workflow` 工具的 schema、结果包络、后台任务、会话记录 | 负责“模型看到什么”，不碰执行             |
| `dsh-workflow-ptc`           | **执行引擎**：在 PTC Node 进程里跑脚本                       | 复用 PTC 运行时的沙箱与进程管理          |
| `dsh-client-ui-workflow-run` | **UI 层**：把持久化记录回放成 run → phase → member 三级树    | 纯回放，不改变模型上下文                 |

设计原则很克制：**脚本、执行、展示、工具调用四件事彻底拆开**。一个 Context 里只允许挂一个引擎，挂第二个会明确报错——所以“换隔离策略”等于“换一个插件”，上层行为不变。

---

## 三、一次 workflow 调用的完整生命周期

1. **模型提交** `{ meta, script, args?, run_in_background? }`。`meta` 是身份数据（`name`、`description`、可选的 `whenToUse`、`phases`）；`script` 是**纯 JavaScript 函数体**（不是 TypeScript，也没有 `export const meta`）；`args` 是 JSON，作为全局变量注入——**永不作为代码求值**。
2. **启动前校验**：meta 结构、脚本能否解析、provider 路由、单次运行上限。任何不合格都在“运行存在”之前同步拒绝，作为**模型可修正的错误**返回。
3. **起进程**：在全新的 Node 进程里执行，受调用方会话**启动时捕获**的文件沙箱策略约束。父级之后切换目录，不会影响这个运行及其后续子级。
4. **脚本跑钩子**：`agent()` 在“本次运行固定的父 agent”下启动受管 subagent Activation。
5. **子代理并发**由运行时计数器控制（不是靠模型自觉）。
6. **脚本 `return` 一个无损 JSON** → 成为运行结果。
7. **工具层阻塞父轮次**直到整个运行结算；模型**只看到最终值和子代理数量，永远看不到中间子代理消息**。
8. **清理**：工具在 `try/finally` 里无条件 `dispose()`；取消信号会桥接到 `run.cancel()`。
9. **记录**：根调用写 4 个 log-only 事件（run-start / 成员开始 / 成员结束 / run-end），UI 靠它们重建那棵树。嵌套调用不写记录。

关键的一条：**运行结果永不 reject**。失败以 `stopReason: 'error'` 兑现，取消以 `'cancelled'` 兑现；但工具层会把“非 completed”映射成 `isError`，**部分输出绝不会被报告为成功**。

---

## 四、脚本能用的 5 个钩子（精确签名）

| 钩子       | 签名                                          | 语义                                                         |
| ---------- | --------------------------------------------- | ------------------------------------------------------------ |
| `agent`    | `agent(prompt, opts?) → Promise<any>`         | 跑一个子代理到底。无 `opts.schema` 返回最终文本；有则返回**校验过的结构化对象**。子代理普通失败返回 `null` |
| `parallel` | `parallel(thunks) → Promise<any[]>`           | 并发跑零参函数，**等全部完成（屏障）**；抛错的 thunk 变 `null` |
| `pipeline` | `pipeline(items, ...stages) → Promise<any[]>` | 每个条目独立过各阶段，**阶段之间无屏障**；`stage(prev, item, index)`；某阶段抛错只把**该条目**丢成 `null` 并跳过后续阶段 |
| `phase`    | `phase(title)`                                | 进度分组，只进观察者事件                                     |
| `log`      | `log(message)`                                | 进度日志，只进观察者事件                                     |

`agent` 的 `opts` 还有：`label`（显示名）、`phase`（归属进度组）、独立的 `provider` / `model` 覆盖。`schema` 支持的是受限 JSON Schema：只用 `type/properties/required/additionalProperties/items/enum/const/oneOf`。

这两个并发原语的分工是有讲究的：**多阶段流水线优先 `pipeline`**（条目 A 进第二段时，条目 B 可以还在第一段），`parallel` 只用在“下一段真的需要所有前序结果凑齐”的场景。这是把数据流编程模型直接搬进了 agent 编排。

一段能跑的最小脚本：

```js
const targets = args.targets
await phase('审查')
const reports = await parallel(targets.map((t) => () =>
  agent(`只审查正确性问题：${t}`, {
    label: t,
    schema: {
      type: 'object',
      properties: {
        ok: { type: 'boolean' },
        issues: { type: 'array', items: { type: 'string' } },
      },
      required: ['ok', 'issues'],
      additionalProperties: false,
    },
  })
))
const bad = reports.filter(Boolean).filter((r) => !r.ok)   // 失败的子代理是 null
await log(`完成 ${reports.length} 份，问题 ${bad.length} 份`)
return { total: targets.length, bad }
```

---

## 五、设计思路：为什么是“代码”，不是“流程画布”

**1. 上下文经济学——这是最核心的动机。**
标准模式里每委派一次，中间状态（起了几个、各自回了什么）都要回写进对话上下文，token 随委派次数**线性增长**，而且模型容易在长链路里忘掉前面的结论。workflow 把循环、分支、重试放进代码，中间值留在脚本变量里。

粗略算一笔账（示意，不是实测）：100 个文件 × 每个子代理回 2000 token 原始输出 ≈ 20 万 token 中间产物。标准模式下这些都要进父上下文；workflow 只回流最后那段聚合 JSON，可能两千 token 都不到。**省的不是模型调用次数，是父上下文的“无用输入”。**

**2. 并发交给运行时，不交给模型。**
`Promise.all` 和循环是确定性的。模型不会“忘记并发”，也不会因为看到第 3 个结果就改主意。

**3. 失败语义分层。**
- 基础设施错误（脚本解析不了、钩子拼错、超上限、进程挂了）→ `WorkflowError` + `fatal`，**终止整个脚本**。理由很直白：拼错的选项不该被静默映射成 `null`。
- 普通子代理失败 → `agent()` 返回 `null`，由脚本自己决定跳过、重试还是汇总。这不算基础设施异常。

**4. 复用 PTC 沙箱，不造第二套隔离。**
workflow 和 `run_code` 共用同一个执行提供方。文件策略只放行 `danger-full-access` 之外的两档（`read-only` / `workspace-write`），不支持约束的运行时直接拒绝。程序可见的环境变量为空。

但官方写得很清楚：**VM 不是安全边界**（VM 里的辅助 API 只是给脚本作者的指引），**文件策略不限制网络**。要网络管控得另配。

**5. 能力最小化。**
`workflow/*` 事件只供观察，payload 是快照，监听器**拿不到 cancel / dispose 权限**；run 归启动它的调用方所有，每条路径都必须 dispose；一个 Context 只能有一个引擎。

**6. 前台/后台双模。**
前台阻塞父轮次，换“模型只看最终结果”；后台注册成 `ctx.jobs` 任务，`phase/log/成员生命周期`实时流进任务输出环，用 `job_kill` 取消。前台是“批处理”，后台是“长任务”。

**7. 刻意不做的事**（比做了什么更能说明设计取舍）：
做完即弃、**无检查点/无恢复**、**无嵌套 `workflow()` 钩子**（禁止递归编排）、无已保存工作流、**无跨子代理 token 预算**、无整体超时。

也就是说：它是“一次性的批处理脚本编排器”，不是 Temporal / LangGraph 那种持久化工作流引擎。想要持久化 DAG，得装第三方插件（插件市场里有 `agent-dag-workflow`），核心刻意不提供。

---

## 六、和 PTC / Ralph / 普通 subagent 的边界

| 能力                 | 编排对象                | 脚本语言      | 默认状态               |
| -------------------- | ----------------------- | ------------- | ---------------------- |
| 普通 `subagent` 工具 | 1–2 个委派              | 无            | 开启                   |
| **`workflow`**       | **几十个子代理 + 阶段** | 纯 JavaScript | 开启                   |
| PTC 模式 `run_code`  | 工具调用（不是代理）    | TypeScript    | 按模式                 |
| Ralph 循环           | 每轮**全新**子代理迭代  | —             | **默认关闭**（实验性） |

系统提示词里的原话很关键：**只有当用户明确要求工作流或大规模多代理编排时才用**，一两个委派优先用普通 subagent。这既是省 token，也是避免“为了用而用”。

---

## 七、你必须知道的限制（尤其你这种大 fan-out）

- 默认并发 `maxConcurrentAgents = 0` → 按 CPU 并行度自动解析（你之前实测约 `min(16, 核数-2)`）；`maxTotalAgents = 1000`；单次 `parallel/pipeline` 最多 4096 项。
- **PTC 的 `maxPendingCalls` 也在吃并发名额**：子代理启动、等结果、资源释放、进度批次都占槽。设并发上限时要留余量。
- 上限是**协作式的**，不是 Host 强制安全配额，也不限制后代 token；**没有整体超时**（只有初始同步片段 5 秒）。
- **不能续跑**：进程重启 = 整个运行丢失，没有中间落盘。这正是大 fan-out 必须自己分批的原因。
- 返回值必须是**无损 JSON**：函数、symbol、循环引用、稀疏数组、`NaN`、嵌套 `undefined` 都会被拒——`return` 前要自己 `map` 干净。
- 后台运行结算前 `job_output` **只有状态、没有中间值**；取消会丢弃局部输出。
- 嵌套 PTC 调用不写会话记录，UI 树里看不到。

---

## 八、怎么选

- **该用**：批量审计 N 个文件、多角度调研、迁移、固定流水线、需要结构化输出的扇出。
- **别用**：只有一两个委派（普通 subagent 更轻）、需要边跑边改计划的探索型任务（前台会阻塞，你看不到中间态）、需要断点续传的超长任务。
- **你的 32–71 子代理场景**：用 `pipeline` + 显式并发上限，比让模型逐个调 subagent 更稳、更省父上下文；但仍要分批并自己落盘，因为引擎不提供恢复。

---

Sources（本机源码包内文档，非二手文章）
- [dsh-workflow 契约层说明](file:///C:/Users/qsevn/AppData/Local/Temp/dsh-extract/_dsh_node_modules_@deepseek-ai_dsh-workflow_README.zh.md)（原路径 `app.asar:/dsh/node_modules/@deepseek-ai/dsh-workflow/README.zh.md`）
- [dsh-tool-workflow 工具层说明](file:///C:/Users/qsevn/AppData/Local/Temp/dsh-extract/_dsh_node_modules_@deepseek-ai_dsh-tool-workflow_README.zh.md)
- [dsh-workflow-ptc 引擎说明](file:///C:/Users/qsevn/AppData/Local/Temp/dsh-extract/_dsh_node_modules_@deepseek-ai_dsh-workflow-ptc_README.zh.md)
- [dsh-client-ui-workflow-run UI 说明](file:///C:/Users/qsevn/AppData/Local/Temp/dsh-extract/_dsh_node_modules_@deepseek-ai_dsh-client-ui-workflow-run_README.zh.md)
- [DSH 官方：PTC 模式定位与运行模式](https://www.jinse.cn/blockchain/3736948.html)
- [Workflow 引擎与 Ralph 循环（社区逆向分析）](https://blog.csdn.net/wshzd/article/details/169165994)
- [插件市场中的持久化 DAG 工作流插件](https://dsh.deepseek404.com/detail.php?id=imsai-sh%2Fawesome-deepseek-harness-plugins)

需要的话，我可以把这套「工具层 → 引擎层 → PTC 进程」的时序画成一张图，或者拿你某个真实的批量任务改写成一段 workflow 脚本做对照。