#!/bin/sh
# 下载静态 ffmpeg + ffprobe 到当前目录，供 deploy/Dockerfile.debian 构建使用。
# 用法：在存放 litepan 二进制的构建目录里执行  sh deploy/fetch-ffmpeg.sh
set -eu

ARCH=$(uname -m)
case "$ARCH" in
    x86_64|amd64)  PKG=ffmpeg-release-amd64-static ;;
    aarch64|arm64) PKG=ffmpeg-release-arm64-static ;;
    *) echo "不支持的架构: $ARCH" >&2; exit 1 ;;
esac

URL="https://johnvansickle.com/ffmpeg/releases/${PKG}.tar.xz"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

echo "[1/3] 下载 $URL"
if ! curl -fL --retry 3 --connect-timeout 20 "$URL" -o "$TMP/ff.tar.xz"; then
    echo "下载失败。国内网络可先挂代理，例如：" >&2
    echo "  curl -fL -x http://127.0.0.1:7890 '$URL' -o /tmp/ff.tar.xz" >&2
    exit 1
fi

echo "[2/3] 解压并取出 ffmpeg / ffprobe"
tar -xJf "$TMP/ff.tar.xz" -C "$TMP"
cp "$TMP"/"$PKG"/ffmpeg  ./ffmpeg
cp "$TMP"/"$PKG"/ffprobe ./ffprobe
chmod +x ffmpeg ffprobe

echo "[3/3] 校验"
./ffmpeg  -hide_banner -version | head -1
./ffprobe -hide_banner -version | head -1
ls -lh ffmpeg ffprobe
echo "完成：现在可以 docker build -f deploy/Dockerfile.debian -t litepan:probe ."
