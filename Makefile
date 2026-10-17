# make builds codebot from this checkout, with the agentcore, litellm and
# mcp-sdk-go that go.work points at, and links it where install.sh puts a
# release, replacing that: from then on every build is the codebot on the
# PATH. Linking asks for sudo where the user cannot write, once.

BIN  := $(CURDIR)/codebot
LINK := /usr/local/bin/codebot
SUDO := $(if $(shell [ -w $(dir $(LINK)) ] && echo w),,sudo)

.PHONY: install build

install: build
	@[ "$$(readlink $(LINK))" = "$(BIN)" ] || $(SUDO) ln -sf $(BIN) $(LINK)

build:
	go build -o $(BIN) ./cmd/codebot
