VERSION ?= 0.1.0
LDFLAGS := -s -w -X github.com/cdotlock/mob-sandbox/pkg/config.Version=$(VERSION)
HOST_OS := $(shell uname -s | tr '[:upper:]' '[:lower:]')
HOST_ARCH := $(shell uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
DIST_DIR := dist
HOST_CLIENT := $(DIST_DIR)/mob-$(HOST_OS)-$(HOST_ARCH)
HOST_SERVER := $(DIST_DIR)/mob-server-$(HOST_OS)-$(HOST_ARCH)
LINUX_CLIENT := $(DIST_DIR)/mob-linux-amd64
LINUX_SERVER := $(DIST_DIR)/mob-server-linux-amd64
HOST_ARCHIVE := $(DIST_DIR)/mob-sandbox_$(VERSION)_$(HOST_OS)_$(HOST_ARCH).tar.gz
LINUX_ARCHIVE := $(DIST_DIR)/mob-sandbox_$(VERSION)_linux_amd64.tar.gz

.PHONY: all build build-linux mob mob-server package release clean install

all: build

build: mob mob-server

mob:
	go build -ldflags "$(LDFLAGS)" -o bin/mob ./cmd/mob

mob-server:
	go build -ldflags "$(LDFLAGS)" -o bin/mob-server ./cmd/mob-server

build-linux:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/mob-linux-amd64 ./cmd/mob
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/mob-server-linux-amd64 ./cmd/mob-server

package: build build-linux
	mkdir -p $(DIST_DIR)
	cp bin/mob $(HOST_CLIENT)
	cp bin/mob-server $(HOST_SERVER)
	cp bin/mob-linux-amd64 $(LINUX_CLIENT)
	cp bin/mob-server-linux-amd64 $(LINUX_SERVER)
	LC_ALL=C LANG=C tar -czf $(HOST_ARCHIVE) -C $(DIST_DIR) mob-$(HOST_OS)-$(HOST_ARCH) mob-server-$(HOST_OS)-$(HOST_ARCH)
	LC_ALL=C LANG=C tar -czf $(LINUX_ARCHIVE) -C $(DIST_DIR) mob-linux-amd64 mob-server-linux-amd64
	LC_ALL=C LANG=C shasum -a 256 $(HOST_CLIENT) $(HOST_SERVER) $(LINUX_CLIENT) $(LINUX_SERVER) $(HOST_ARCHIVE) $(LINUX_ARCHIVE) > $(DIST_DIR)/checksums.txt

release: package

clean:
	rm -rf bin/ $(DIST_DIR)/

install: build
	cp bin/mob /usr/local/bin/mob
	cp bin/mob-server /usr/local/bin/mob-server
