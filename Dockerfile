# Build
FROM golang:1.22-alpine AS build
WORKDIR /src
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" -o /out/sub2api-monitor ./cmd/monitor

# Runtime
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata \
  && adduser -D -H -u 10001 monitor
USER monitor
WORKDIR /app
COPY --from=build /out/sub2api-monitor /app/sub2api-monitor
COPY config.example.yaml /app/config.example.yaml
ENV TZ=Asia/Shanghai
ENTRYPOINT ["/app/sub2api-monitor"]
CMD ["-config", "/app/config.yaml"]
