#!/usr/bin/env python3
"""文档相对链接检查（scripts/doclinks.pl 的 Python 等价实现）。

git 跟踪的 .md 里 [文字](目标) 的相对目标必须存在。跳过围栏代码块、行内代码、
外部链接（带协议）与页内锚点（#…）；不查锚点本身。

用法：在仓库根运行 `python3 scripts/doclinks.py`，有坏链时退出码 1。
需要 perl 缺失时用这个（Windows 上常缺）。
"""
import os
import re
import subprocess
import sys
from urllib.parse import unquote


def main():
    raw = subprocess.run(
        ['git', '-c', 'core.quotepath=false', 'ls-files', '-z', '--', '*.md'],
        capture_output=True).stdout
    files = [f for f in raw.decode('utf-8', 'surrogateescape').split('\0') if f]
    bad = 0
    checked = 0
    for f in files:
        try:
            with open(f, encoding='utf-8') as fh:
                lines = fh.read().splitlines()
        except (OSError, UnicodeDecodeError):
            continue
        base = os.path.dirname(f)
        fence = ''
        for n, line in enumerate(lines, 1):
            m = re.match(r'^\s*(`{3,}|~{3,})', line)
            if m:
                mark = m.group(1)
                if fence == '':
                    fence = mark[0] * len(mark)
                elif mark[0] == fence[0] and len(mark) >= len(fence):
                    fence = ''
                continue
            if fence:
                continue
            line = re.sub(r'(`+).*?\1', '', line)  # 行内代码
            for tm in re.finditer(r'\]\((<[^>]+>|[^)\s]+)\)', line):
                t = tm.group(1)
                if t.startswith('<') and t.endswith('>'):
                    t = t[1:-1]
                if re.match(r'^[a-zA-Z][a-zA-Z0-9+.-]*:', t) or t.startswith('#'):
                    continue
                t = re.sub(r'#.*$', '', t)
                if not t:
                    continue
                t = unquote(t)
                path = t[1:] if t.startswith('/') else os.path.join(base, t)
                path = os.path.normpath(path)
                checked += 1
                if not os.path.exists(path):
                    print(f'  {f}:{n} → {t}')
                    bad += 1
    print(f'检查了 {checked} 个相对链接')
    if bad:
        print(f'坏链 {bad} 处')
        return 1
    print('OK：没有坏链')
    return 0


if __name__ == '__main__':
    sys.exit(main())
