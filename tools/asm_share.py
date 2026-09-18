#!/usr/bin/env python3
"""按指令类型统计 Go 汇编，定位「寄存器压力」型瓶颈。

为什么需要它：pprof 只按函数归属，会把「有效位运算」和「XMM/栈搬运」混在同一行，
于是看不出「这笔时间花在什么上」。本项目用它在 `updateThreats` 里定位到
`MOVUPS 254 条(17.8%) vs 位运算 73 条(5%)` —— 16 字节的 `bitboard`（[2]uint64）
在寄存器不够时被溢出到栈，把位棋盘拆成 lo/hi 两个 uint64 后实测三处共 +16%。

判读方法：
  - **搬运占大头、位运算占小头** ⇒ 数据表示/寄存器压力问题，优化算法是白费力气；
  - 位运算占大头 ⇒ 才该考虑换算法或向量化。

用法：
    python tools/asm_share.py                     # 默认扫 nnue/search/game 三个包
    python tools/asm_share.py --filter Threats    # 只看名字含 Threats 的函数
    python tools/asm_share.py --top 30 --min-instr 100
"""

import collections
import re
import subprocess
import sys

PKGS = ["./internal/nnue/", "./internal/search/", "./internal/game/"]

TEXT_RE = re.compile(r"^[\w\.\(\)\*\[\]/<>-]+\s+STEXT\b")
OP_RE = re.compile(r"\t([A-Z][A-Z0-9]+)\t")
BLOCK_RE = re.compile(r"^([\w\.\(\)\*\[\]/<>-]+)\s+STEXT\b")

MOV_OPS = ("MOV", "MOVUPS", "MOVOU", "MOVL", "MOVB", "MOVW", "MOVQ", "MOVSD", "MOVSS", "VEXTRACT", "VINSERT", "VPUNPCK")
ALU_OPS = ("ORQ", "ORL", "ANDQ", "ANDL", "ANDNQ", "XORQ", "XORL", "NOTQ", "NOTL", "SHLQ", "SHRQ", "SARQ", "BTSQ", "BTRQ", "ADCQ", "SBBQ")
SLOT_OPS = ("PBROADCAST", "VPCM", "PCMP", "VPSLL", "VPSRL", "VPSUB", "VPADD", "VPMADD", "VPSHUF", "PSHUF", "PUNPCK", "PAND", "POR", "PXOR", "PADD", "PSUB")


def dump(pkgs):
    out = subprocess.run(
        ["go", "build", "-gcflags=-S"] + pkgs,
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
    ).stdout.decode("utf-8", "replace")
    return out


def split_blocks(text):
    blocks, cur = {}, None
    for ln in text.split("\n"):
        m = BLOCK_RE.match(ln)
        if m:
            cur = m.group(1)
            blocks.setdefault(cur, [])
            continue
        if TEXT_RE.match(ln) or ln.startswith("TEXT "):
            cur = None
            continue
        if cur is not None:
            blocks[cur].append(ln)
    return blocks


def classify(counter):
    total = sum(counter.values())
    mov = sum(n for op, n in counter.items() if any(op.startswith(p) for p in MOV_OPS))
    alu = sum(n for op, n in counter.items() if op in ALU_OPS)
    simd = sum(n for op, n in counter.items() if any(op.startswith(p) for p in SLOT_OPS))
    br = sum(n for op, n in counter.items() if op.startswith("J") or op in ("CALL", "RET"))
    return total, mov, alu, simd, br


def main():
    args = sys.argv[1:]
    top, min_instr, filt = 20, 60, ""
    for i, a in enumerate(args):
        if a == "--top":
            top = int(args[i + 1])
        elif a == "--min-instr":
            min_instr = int(args[i + 1])
        elif a == "--filter":
            filt = args[i + 1]

    blocks = split_blocks(dump(PKGS))

    rows = []
    for name, body in blocks.items():
        if filt and filt not in name:
            continue
        counter = collections.Counter()
        for ln in body:
            m = OP_RE.search(ln)
            if m:
                counter[m.group(1)] += 1
        if not counter:
            continue
        total, mov, alu, simd, br = classify(counter)
        if total < min_instr:
            continue
        rows.append((name, total, mov, alu, simd, br, counter))

    # 按「搬运占比 × 规模」排序：这是寄存器压力的可疑度。
    rows.sort(key=lambda r: -(r[2] / r[1] * r[1]))

    print("%-52s %6s %7s %7s %7s %7s %7s" %
          ("函数", "指令", "MOV*", "MOVUPS", "位运算", "搬运%", "位运算%"))
    print("-" * 100)
    for name, total, mov, alu, simd, br, counter in rows[:top]:
        short = name.split("/")[-1]
        if len(short) > 50:
            short = "…" + short[-49:]
        print("%-52s %6d %7d %7d %7d %6.0f%% %7.0f%%" %
              (short, total, mov, counter.get("MOVUPS", 0), alu, 100 * mov / total, 100 * alu / total))

    print()
    print("判读：搬运占大头而位运算占小头 ⇒ 数据表示/寄存器压力问题（考虑拆 lo/hi）；")
    print("      位运算占大头 ⇒ 才值得考虑换算法或向量化。")
    print("      也可以--filter <函数名> 看单个函数的 opcode 分布。")


if __name__ == "__main__":
    main()
