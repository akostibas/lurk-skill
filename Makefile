VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
SKILL_DIR ?= $(HOME)/.claude/skills/lurk

LDFLAGS := -X main.version=$(VERSION)

# The SQLCipher amalgamation isn't in git (see bin/vendor-sqlcipher.sh) — every
# build target depends on it so a fresh clone just works.
SQLCIPHER := internal/signal/sqlite3.c

.PHONY: build install test vendor clean distclean

build: $(SQLCIPHER)
	go build -ldflags "$(LDFLAGS)" -o lurk ./cmd/lurk

# The binary lives inside the skill dir: the skill is then self-contained, and
# `update.sh` can replace binary and instructions together.
install: $(SQLCIPHER)
	mkdir -p "$(SKILL_DIR)"
	go build -ldflags "$(LDFLAGS)" -o "$(SKILL_DIR)/lurk" ./cmd/lurk
	cp skill/SKILL.md "$(SKILL_DIR)/SKILL.md"
	cp skill/update.sh "$(SKILL_DIR)/update.sh"
	chmod +x "$(SKILL_DIR)/update.sh"
	@echo "Installed lurk $(VERSION) -> $(SKILL_DIR)"
	@echo "For terminal use: ln -sf $(SKILL_DIR)/lurk ~/bin/lurk"

test: $(SQLCIPHER)
	go test ./...

vendor:
	bin/vendor-sqlcipher.sh --force

$(SQLCIPHER):
	bin/vendor-sqlcipher.sh

clean:
	rm -f lurk

# Also drops the vendored SQLCipher sources; the next build re-fetches them.
distclean: clean
	rm -f internal/signal/sqlite3.c internal/signal/sqlite3.h \
	      internal/signal/sqlite3ext.h internal/signal/.sqlcipher-version
