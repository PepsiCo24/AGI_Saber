# ===== 本地预编译 + 轻量运行镜像 =====
# 不在 Docker 中编译，避免拉取 golang 大镜像
#
# 编译命令（按本机 CPU 架构选 GOARCH，Apple Silicon=arm64，Intel=amd64）：
#   CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o final-agent ./cmd/server   # M 系列 Mac
#   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o final-agent ./cmd/server   # Intel/AMD64

FROM swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/alpine:3.19

RUN apk add --no-cache ca-certificates tzdata curl \
    && cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime \
    && echo "Asia/Shanghai" > /etc/timezone

WORKDIR /app

COPY final-agent .

EXPOSE 8090

ENTRYPOINT ["/app/final-agent"]
