#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
生成「熊猫象棋」fnOS 应用图标。
设计要点（一眼看出是象棋）：
  - 棋盘绿底圆角瓦片（透明圆角外）
  - 中央红方圆形棋子：外圈 + 内圈双环（象棋棋子经典造型）
  - 棋子中心粗体汉字「帥」（红方主帅，最具辨识度的象棋符号）
  - 顶部一对黑色小熊猫耳，点明「熊猫」品牌
高分渲染后用 LANCZOS 降采样，保证 64px 小图标边缘清晰。
"""
from PIL import Image, ImageDraw, ImageFont
import os

S = 1024  # 主画布分辨率
FONT = "C:/Windows/Fonts/simhei.ttf"

# 配色
TILE_GREEN   = (24, 107, 69, 255)    # 棋盘绿
TILE_GREEN2  = (18, 92, 58, 255)     # 暗部
PIECE_RED    = (210, 59, 46, 255)    # 红方棋子
RIM_DARK     = (122, 26, 18, 255)    # 棋子描边/内圈（暗红）
CHAR_CREAM   = (251, 241, 223, 255)  # 棋子字面（米白）
PANDA_BLACK  = (20, 20, 20, 255)     # 熊猫耳

HERE = os.path.dirname(os.path.abspath(__file__))

def make_master():
    img = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)

    # 1) 棋盘绿底圆角瓦片（透明圆角外）
    tile_r = 200
    d.rounded_rectangle([0, 0, S, S], radius=tile_r, fill=TILE_GREEN)
    # 底部暗角，增加立体感
    d.rounded_rectangle([0, S*0.62, S, S], radius=tile_r, fill=TILE_GREEN2)
    # 顶部高光带
    d.rounded_rectangle([0, 0, S, S*0.18], radius=tile_r, fill=(40, 130, 88, 255))

    cx, cy = S // 2, int(S * 0.55)   # 棋子中心（略偏下，给顶部熊猫耳留位）
    R = int(S * 0.345)               # 棋子外半径

    # 2) 熊猫耳（先画，再被棋子压住下半，只露上半 → 像耳朵）
    ear_r = int(S * 0.092)
    ear_y = int(cy - R * 0.92)
    ear_dx = int(R * 0.62)
    for ex in (cx - ear_dx, cx + ear_dx):
        d.ellipse([ex - ear_r, ear_y - ear_r, ex + ear_r, ear_y + ear_r],
                  fill=PANDA_BLACK)
        # 耳内浅灰，增加层次
        d.ellipse([ex - ear_r*0.55, ear_y - ear_r*0.55,
                   ex + ear_r*0.55, ear_y + ear_r*0.55],
                  fill=(60, 60, 60, 255))

    # 3) 红方棋子圆盘
    d.ellipse([cx - R, cy - R, cx + R, cy + R], fill=PIECE_RED)
    # 顶面高光（左上椭圆）
    d.ellipse([cx - R*0.7, cy - R*0.72, cx - R*0.1, cy - R*0.18],
              fill=(225, 95, 84, 255))

    # 4) 双圈（外圈粗、内圈细）——象棋棋子经典
    d.ellipse([cx - R, cy - R, cx + R, cy + R], outline=RIM_DARK, width=int(S*0.022))
    rin = int(R * 0.80)
    d.ellipse([cx - rin, cy - rin, cx + rin, cy + rin], outline=RIM_DARK, width=int(S*0.014))

    # 5) 棋子汉字「帥」
    font = ImageFont.truetype(FONT, int(R * 1.18))
    d.text((cx, cy + int(R*0.02)), "帥", font=font, fill=CHAR_CREAM, anchor="mm")
    return img

def save_scaled(master, out_path, size):
    out = master.resize((size, size), Image.LANCZOS)
    out.save(out_path)
    print(f"wrote {out_path} ({size}x{size})")

def main():
    master = make_master()
    # 先存一张 512 预览
    save_scaled(master, os.path.join(HERE, "ICON_PREVIEW.png"), 512)
    save_scaled(master, os.path.join(HERE, "ICON.PNG"), 64)
    save_scaled(master, os.path.join(HERE, "ICON_256.PNG"), 256)
    save_scaled(master, os.path.join(HERE, "app", "ui", "images", "icon_64.png"), 64)
    save_scaled(master, os.path.join(HERE, "app", "ui", "images", "icon_256.png"), 256)

if __name__ == "__main__":
    main()
