#!/bin/sh
# 首次启动时把内置 ffmpeg 播种到数据卷（卷里已有则不动，尊重用户自备的版本）
set -e

if [ ! -e /app/data/tools/ffmpeg ] && [ -x /opt/ffmpeg/ffmpeg ]; then
    mkdir -p /app/data/tools
    cp /opt/ffmpeg/ffmpeg /app/data/tools/ffmpeg
    chmod +x /app/data/tools/ffmpeg
    echo "[entrypoint] 已播种内置 ffmpeg 到 /app/data/tools/ffmpeg"
fi

exec /app/litepan
