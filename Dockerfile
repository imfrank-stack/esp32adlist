# syntax=docker/dockerfile:1
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
COPY config ./config
# 带上本地规则源副本，构建阶段即可离线生成 data/dist，不需要联网
COPY data/raw ./data/raw
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/builder ./cmd/builder
RUN mkdir -p /dist && /out/builder -prepare never -config config/sources.json -out /dist -basedir /src

FROM alpine:3.20
RUN adduser -D -u 10001 app && apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/server /app/server
COPY --from=build /out/builder /app/builder
COPY --from=build /dist /app/data/dist
COPY config /app/config
USER app
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1
# 默认直接跑服务；需要重建列表时可替换入口为：/app/builder -config /app/config/sources.json -out /app/data/dist -basedir /app
ENTRYPOINT ["/app/server", "-addr", ":8080", "-dir", "/app/data/dist"]
