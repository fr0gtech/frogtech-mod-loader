GO ?= go
VERSION ?= $(shell cat VERSION)

.PHONY: build test vet clean
build:
	GO=$(GO) ./build.sh $(VERSION)
test:
	$(GO) test ./...
vet:
	$(GO) vet ./...
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GO) vet ./...
clean:
	rm -rf dist rsrc_windows_amd64.syso
