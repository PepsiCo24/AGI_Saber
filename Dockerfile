# ===== 本地预编译 + 轻量运行镜像 =====
# 不在 Docker 中编译，避免拉取 golang 大镜像
# 本地编译命令: CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -C . -ldflags="-s -w" -o final-agent .

FROM swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/alpine:3.19

RUN apk add --no-cache ca-certificates tzdata curl \
    && cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime \
    && echo "Asia/Shanghai" > /etc/timezone

WORKDIR /app

COPY final-agent .
COPY frontend/ ./frontend/

EXPOSE 8090

ENTRYPOINT ["/app/final-agent"]
