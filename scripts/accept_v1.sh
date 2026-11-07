#!/usr/bin/env bash
# v1 整体验收（计划/01 第三节第 2 条）里能自动跑的部分，真实模型（key 读自 framework/.env，不回显）：
#   1–4 经应用接口层驱动（同桌面端 / 网页端的路径）：单聊、主会话调子智能体、工作组组长派活并收回结果、
#       只读上限的智能体委派写文件被拦下 —— client/internal/boot/live_test.go
#   5   回合中途杀进程，重启后续跑 —— accept_m1.sh
# 桌面端界面单聊与 explain trace 看守卫拒绝原因要人工看。
# 用法：bash scripts/accept_v1.sh（在 framework/ 下执行）
set -u
cd "$(dirname "$0")/.."
set -a; . ./.env; set +a
echo "===== 1–4（client/internal/boot TestLive）"
( cd ../client && TENON_LIVE=1 go test -run TestLive -v -count=1 -timeout 30m ./internal/boot/ ) || echo "FAIL: TestLive"
echo "===== 5 杀进程后续跑（accept_m1.sh，看输出里 run2 是否接着把任务做完）"
bash scripts/accept_m1.sh
