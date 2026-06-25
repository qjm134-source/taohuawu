# 构建阶段
FROM golang:1.25-alpine AS builder

WORKDIR /app

# 安装依赖
RUN apk add --no-cache git

# 设置 Go 代理为国内镜像
ENV GOPROXY=https://goproxy.cn,direct

# 复制 go.mod 和 go.sum
COPY go.mod go.sum ./
RUN go mod download

# 复制源代码
COPY . .

# 构建
RUN CGO_ENABLED=0 GOOS=linux go build -o server ./cmd/server

# 运行阶段
FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /app

# 复制二进制文件、数据和配置
COPY --from=builder /app/server .
COPY --from=builder /app/data ./data
COPY --from=builder /app/configs ./configs

# 创建日志目录
RUN mkdir -p /app/logs

# 暴露端口
EXPOSE 8080

# 运行
CMD ["./server"]
