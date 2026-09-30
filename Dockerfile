# ---- build stage -----------------------------------------------------------
# The gateway is pure Go standard library, so the build needs no module
# downloads and the runtime stage can stay minimal.
FROM golang:1.24-alpine AS build

WORKDIR /src

COPY go.mod ./
COPY main.go ./
COPY internal ./internal

ARG VERSION=1.0.0
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags="-s -w -X main.version=${VERSION}" \
        -o /out/astudio2api .

# ---- runtime stage ---------------------------------------------------------
FROM alpine:3.20

# ca-certificates: TLS to agent.xfyun.cn / maas-api.xf-yun.com
# tzdata: correct local time for the scheduled check-in hour
RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -S astudio \
 && adduser -S -G astudio -h /home/astudio astudio \
 && mkdir -p /data \
 && chown -R astudio:astudio /data

COPY --from=build /out/astudio2api /usr/local/bin/astudio2api

USER astudio:astudio

ENV ASTUDIO_HOST=0.0.0.0 \
    ASTUDIO_PORT=10086 \
    ASTUDIO_DATA_PATH=/data/astudio2api-data.json \
    TZ=Asia/Shanghai

VOLUME ["/data"]
EXPOSE 10086

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- "http://127.0.0.1:${ASTUDIO_PORT}/ping" >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/usr/local/bin/astudio2api"]
