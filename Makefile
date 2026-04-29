VERSION ?= 0.1.0
LDFLAGS := -s -w -X github.com/cdotlock/mob-sandbox/pkg/config.Version=$(VERSION)

.PHONY: all build build-linux mob mob-server clean install

all: build

build: mob mob-server

mob:
	go build -ldflags "$(LDFLAGS)" -o bin/mob ./cmd/mob

mob-server:
	go build -ldflags "$(LDFLAGS)" -o bin/mob-server ./cmd/mob-server

build-linux:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/mob-linux-amd64 ./cmd/mob
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/mob-server-linux-amd64 ./cmd/mob-server

clean:
	rm -rf bin/

install: build
	cp bin/mob /usr/local/bin/mob
	cp bin/mob-server /usr/local/bin/mob-server
