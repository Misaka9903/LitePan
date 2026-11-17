# LitePan 探测版 · Docker 部署

自带媒体信息探测：整理时读文件头补全分辨率/编码/声道，不再只依赖文件名。

## 构建镜像

构建上下文需要四样东西：

```
<context>/
├── litepan              # 本分支编译出的二进制（CGO_ENABLED=0）
├── ffmpeg               # 静态版 ffmpeg
├── ffprobe              # 静态版 ffprobe（与 ffmpeg 同版本）
└── entrypoint.sh        # 本目录下的入口脚本
```

Dockerfile 用 `deploy/Dockerfile.debian`。静态 ffmpeg/ffprobe 可从
https://johnvansickle.com/ffmpeg/ 的 amd64 包解压取：

```bash
curl -fLO https://johnvansickle.com/ffmpeg/releases/ffmpeg-release-amd64-static.tar.xz
tar -xJf ffmpeg-release-amd64-static.tar.xz
cp ffmpeg-*-amd64-static/ffmpeg ffmpeg-*-amd64-static/ffprobe .
```

```bash
CGO_ENABLED=0 go build -o litepan ./cmd/litepan
cp ../deploy/entrypoint.sh .
docker build -f ../deploy/Dockerfile.debian -t litepan:probe .
```

## 为什么需要 ffprobe

探测优先调用 ffprobe 走 JSON，能读到 `pix_fmt`、`bits_per_raw_sample`、`color_transfer`
和 `side_data_list`，所以能区分：

- `DoVi P5 / P7 / P8`（流媒体单层、蓝光双层、兼容 HDR10 的单层）
- `HDR10+ / HDR10 / HLG / SDR`
- `10-bit / 12-bit`

只有 ffmpeg 时程序会回退到文本解析：DoVi 丢掉 Profile，HDR10+ 可能被当成 HDR10。
启动日志会写明当前用的是哪一种：

```
level=INFO msg="媒体信息探测使用 ffprobe（JSON）" path=/app/data/tools/ffprobe
level=WARN msg="媒体信息探测回退 ffmpeg 文本解析：未找到 ffprobe，动态范围/色深可能不准"
```

镜像内 `/opt/ffmpeg/ffprobe` 同时软链到 `/usr/local/bin/ffprobe`，数据卷被清空也仍能在 PATH 找到。

## 启动

```bash
docker compose up -d
```

打开 `http://127.0.0.1:5211`，默认账号 `admin / admin`（首次登录强制改密）。

## 首次使用必做

1. **打开探测开关**：媒体整理 → 媒体信息标签排序 → 「探测真实媒体信息」→ 开启
   （默认关闭，不开的话整理结果与上游版本完全一样，没有任何画质标签）
2. 添加网盘账号

## 网络模式（重要）

compose 里用的是 `network_mode: host`，不是端口映射。原因：

- 容器若用默认 bridge，容器内的 `127.0.0.1` 是容器自己，
  在「媒体整理 → TMDB 设置」里填宿主代理（如 `http://127.0.0.1:47831`）会**连不上**
- 改成 host 网络后，容器与宿主共享网络命名空间，`127.0.0.1` 即宿主，代理可用

副作用：5211 直接监听在宿主所有网卡上，compose 里不能再写 `ports`。

## 说明

- 镜像内置 ffmpeg / ffprobe 在 `/opt/ffmpeg`，首次启动会复制到数据卷 `data/tools/`；
  卷里已有则**不覆盖**（想换版本直接替换该文件，重启生效）
- ffprobe 还有一个 `/usr/local/bin/ffprobe` 软链指向镜像内的副本，数据卷被清空也不会回退到文本解析
- 容器以 uid 1000 运行，数据卷文件属主就是 1000；宿主用户不在 1000 时改 compose 里的 `user`
- 数据目录挂载会**遮住**镜像里的 `/app/data`，所以 ffmpeg 放在 `/opt` 而不是 `/app/data`
