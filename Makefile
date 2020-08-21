# メタ情報
LDFLAGS := -ldflags="-s -w -extldflags \"-static\""

export GO111MODULE=on

# 必要なツール類をセットアップする
## Install for Development
.PHONY: devel-deps
devel-deps: deps
	GO111MODULE=off go get -u \
	  golang.org/x/lint/golint

## Clean binaries
.PHONY: clean
clean:
	rm -rf bin/*

## Run tests
.PHONY: test
test: deps
	go test -cover -v ./...

## Install dependencies
.PHONY: deps
deps:
	go get ./...

## Update dependencies
.PHONY: update
update:
	go get -u -d ./...
	go mod tidy -v

## Run Lint
.PHONY: lint
lint: devel-deps
	go vet ./...
	golint -set_exit_status ./...

## Build binaries ex. make bin/fast-autoscaler
build: *.go
	go build -a -tags netgo -installsuffix netgo $(LDFLAGS) -o bin/fast-autoscaler $^

