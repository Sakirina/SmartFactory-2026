TOOLS_BIN ?= $(CURDIR)/.tools/bin
GOPATH ?= $(CURDIR)/.cache/gopath
GOCACHE ?= $(CURDIR)/.cache/go-build
GOMODCACHE ?= $(CURDIR)/.cache/go-mod
GOENV := GOPATH=$(GOPATH) GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE)
PATH := $(TOOLS_BIN):$(PATH)

PROTO_FILES := proto/datatransfer/v1/datatransfer.proto proto/datatransfer/plugin/v1/plugin.proto

.PHONY: tools proto test

tools:
	mkdir -p $(TOOLS_BIN)
	$(GOENV) GOBIN=$(TOOLS_BIN) go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	$(GOENV) GOBIN=$(TOOLS_BIN) go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

proto: tools
	protoc --proto_path=. \
		--go_out=. --go_opt=module=competition2026/product/datatransfer \
		--go-grpc_out=. --go-grpc_opt=module=competition2026/product/datatransfer \
		$(PROTO_FILES)

test:
	$(GOENV) go test ./...

tidy:
	$(GOENV) go mod tidy
