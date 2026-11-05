// 会话组件（L2）：消息、思考、工具步骤、回合尾巴、交互卡、输入框、消息列表。
// 不依赖协议客户端与工作台：数据经 props 传入（形状见 types.ts，wire.gen.ts 的类型在结构上满足），
// 扩展位置以 render props / slot 函数留出，shell 把注册表接上；单独嵌入时也能直接用。

export * from "./types";
export * from "./messages";
export * from "./turn";
export * from "./interaction";
export * from "./composer";
export * from "./list";
