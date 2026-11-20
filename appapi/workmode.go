package appapi

// 工作方式（客户端改进 M12，docs/50 第 5.2 节 Plan 模式）：session.settings {work_mode}、Settings.work_mode、
// kind = plan 的计划卡交互（由 plan-mode 插件的 exit_plan 发起，回答 response.value = wire.PlanAnswer）。

// CapWorkMode hello 能力：支持 work_mode。
const CapWorkMode = "work_mode"
