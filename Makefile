VERSION ?= 0.1.0
LDFLAGS := -s -w -X github.com/720pixel/RedSync/internal/cli.Version=$(VERSION)
GOFLAGS := -mod=mod

.PHONY: build linux windows clean run

build:
	GOFLAGS=$(GOFLAGS) go build -ldflags "$(LDFLAGS)" -o RedSync .

# cross builds. cgo stays off so these are static and need no system libs.
linux:
	GOFLAGS=$(GOFLAGS) CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
		go build -ldflags "$(LDFLAGS)" -o dist/RedSync .

windows:
	GOFLAGS=$(GOFLAGS) CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
		go build -ldflags "$(LDFLAGS)" -o dist/RedSync.exe .

release: linux windows

clean:
	rm -rf RedSync RedSync.exe dist work
