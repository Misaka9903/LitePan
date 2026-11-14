# LitePan 探测版 · Docker 部署

自带媒体信息探测：整理时读文件头补全分辨率/编码/声道，不再只依赖文件名。

## 构建镜像

构建上下文需要三样东西：

```
<context>/
├── Dockerfile.probe     # 本目录上一级同名文件
├── litepan              # 本分支编译出的二进制（CGO_ENABLED=0）
└── ffmpeg               # 静态版 ffmpeg（7.0.2 即可）
```

```bash
CGO_ENABLED=0 go build -o litepan ./cmd/litepan
cp ../Dockerfile.probe .
docker build -f Dockerfile.probe -t litepan:probe .
```

## 启动

```bash
docker compose up -d
```

打开 `http://127.0.0.1:5211`，默认账号 `admin / admin`（首次登录强制改密）。

## 首次使用必做

1. **打开探测开关**：媒体整理 → 媒体信息标签排序 → 「探测真实媒体信息」→ 开启
   （默认关闭，不开的话整理结果与上游版本完全一样，没有任何画质标签）
2. 添加网盘账号

## 说明

- 镜像内置 ffmpeg 在 `/opt/ffmpeg`，首次启动会复制到数据卷 `data/tools/ffmpeg`；
  卷里已有则**不覆盖**（想换版本直接替换该文件）
- 容器以 uid 1000 运行，数据卷文件属主就是 1000；宿主用户不在 1000 时改 compose 里的 `user`
- 数据目录挂载会**遮住**镜像里的 `/app/data`，所以 ffmpeg 放在 `/opt` 而不是 `/app/data`
