APP_NAME := sub2api-monitor
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: build run test tidy fmt docker

build:
	@mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o bin/$(APP_NAME) ./cmd/monitor

run:
	go run ./cmd/monitor -config config.yaml

test:
	go test ./...

tidy:
	go mod tidy

fmt:
	gofmt -w ./cmd ./internal

docker:
	docker build -t $(APP_NAME):$(VERSION) .


# Cross-compile release archives into dist/ (mirrors CI matrix).
.PHONY: release-binaries
release-binaries:
	@mkdir -p dist
	@VERSION="$(VERSION)" COMMIT="$(COMMIT)" DATE="$(DATE)"; \
	LDFLAGS="-s -w -X main.version=$$VERSION -X main.commit=$$COMMIT -X main.date=$$DATE"; \
	for pair in "linux amd64" "linux arm64" "darwin amd64" "darwin arm64" "windows amd64"; do \
	  set -- $$pair; GOOS=$$1 GOARCH=$$2; \
	  out="sub2api-monitor_$${VERSION}_$${GOOS}_$${GOARCH}"; \
	  ext=""; [ "$$GOOS" = "windows" ] && ext=".exe"; \
	  echo "==> $$GOOS/$$GOARCH"; \
	  CGO_ENABLED=0 GOOS=$$GOOS GOARCH=$$GOARCH go build -trimpath -ldflags "$$LDFLAGS" -o "dist/$${out}$${ext}" ./cmd/monitor; \
	  if [ "$$GOOS" = "windows" ]; then (cd dist && zip -9 "$${out}.zip" "$${out}$${ext}" && rm -f "$${out}$${ext}"); \
	  else (cd dist && tar -czf "$${out}.tar.gz" "$${out}$${ext}" && rm -f "$${out}$${ext}"); fi; \
	done; \
	(cd dist && sha256sum * > SHA256SUMS.txt || shasum -a 256 * > SHA256SUMS.txt); \
	ls -lah dist
