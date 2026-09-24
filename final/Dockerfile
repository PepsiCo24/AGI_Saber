# ===== Python 运行镜像 =====
FROM python:3.11-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    PIP_NO_CACHE_DIR=1 \
    TZ=Asia/Shanghai

# 系统依赖：tzdata + psycopg2-binary 运行时所需的 libpq、curl 用于 healthcheck
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata curl \
    && rm -rf /var/lib/apt/lists/* \
    && ln -sf /usr/share/zoneinfo/Asia/Shanghai /etc/localtime \
    && echo "Asia/Shanghai" > /etc/timezone

WORKDIR /app

# 先装依赖以便利用 Docker 层缓存
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# 拷贝源码
COPY config/ ./config/
COPY internal/ ./internal/
COPY main.py ./
COPY frontend/ ./frontend/

ENV FRONTEND_DIR=/app/frontend \
    AGI_CONFIG=/app/config/config.yaml

EXPOSE 8090

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD curl -fsS http://localhost:8090/health || exit 1

ENTRYPOINT ["python", "-u", "main.py"]
