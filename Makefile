# Всё собирается в Docker: на хосте нужен только docker.
GO_IMAGE   := golang:1.27.3
NODE_IMAGE := node:22-alpine
APP        := dist/icloud-export.app
VERSION    ?= 0.1.0

DOCKER_GO = docker run --rm -v "$(CURDIR)":/src -w /src \
	-v icloud-export-gomod:/go/pkg/mod -v icloud-export-gocache:/root/.cache/go-build \
	$(GO_IMAGE)
DOCKER_NODE = docker run --rm -v "$(CURDIR)/web":/web -v "$(CURDIR)/internal/web/dist":/out -w /web \
	-v icloud-export-npm:/root/.npm $(NODE_IMAGE)

DARWIN_ENV = env CGO_ENABLED=0 GOOS=darwin GOARCH=arm64

.PHONY: app web bin test test-darwin tidy clean

app: web bin
	$(DOCKER_GO) sh -c 'go run ./build/mkapp -bin dist/icloud-export -out $(APP) -version $(VERSION)'
	@echo "built $(APP)"

web:
	$(DOCKER_NODE) sh -c 'npm ci --no-audit --no-fund && OUT_DIR=/out npm run build && touch /out/.gitkeep'

bin:
	$(DOCKER_GO) $(DARWIN_ENV) go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o dist/icloud-export ./cmd/icloud-export

test:
	$(DOCKER_GO) sh -c 'gofmt -l -w cmd internal build && go vet ./... && go test -race ./...'

# darwin-специфичные тесты (birthtime): собираем в контейнере, запускаем на хосте
test-darwin:
	$(DOCKER_GO) sh -c '$(DARWIN_ENV) go test -c -o dist/meta-darwin.test ./internal/meta && $(DARWIN_ENV) go test -c -o dist/export-darwin.test ./internal/export'
	./dist/meta-darwin.test
	./dist/export-darwin.test -test.run Darwin -test.v

tidy:
	$(DOCKER_GO) go mod tidy

clean:
	rm -rf dist
