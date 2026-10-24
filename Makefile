VERSION ?= 0.1.0
LDFLAGS := -s -w -X main.version=$(VERSION)

build:
	go build -ldflags "$(LDFLAGS)" -o bin/tawanzu ./cmd/tawanzu

release:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o dist/tawanzu-linux-amd64 ./cmd/tawanzu
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o dist/tawanzu-linux-arm64 ./cmd/tawanzu

test:
	go vet ./... && go test ./...

install: build
	install -m 0755 bin/tawanzu /usr/local/bin/tawanzu
