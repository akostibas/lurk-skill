VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
SKILL_DIR ?= $(HOME)/.claude/skills/lurk
BIN_DIR   ?= $(HOME)/bin

LDFLAGS := -X main.version=$(VERSION)

# SKILLS_MANAGED=1: an outside tool (e.g. a skill sync) keeps lurk current, so
# leave out update.sh and build without the upgrade notice.
ifeq ($(SKILLS_MANAGED),1)
LDFLAGS  += -X main.updateCheck=off
UPDATE_SH :=
else
UPDATE_SH := skill/update.sh
endif

# The SQLCipher amalgamation isn't in git (see bin/vendor-sqlcipher.sh) — every
# build target depends on it so a fresh clone just works.
SQLCIPHER := internal/signal/sqlite3.c

.PHONY: build install test vendor clean distclean

build: $(SQLCIPHER)
	go build -ldflags "$(LDFLAGS)" -o lurk ./cmd/lurk

# The binary lives inside the skill dir: the skill is then self-contained, and
# `update.sh` can replace binary and instructions together. Builds before
# touching the install, so a failed build leaves the old one working.
install: $(SQLCIPHER)
	go build -ldflags "$(LDFLAGS)" -o lurk ./cmd/lurk
	rm -rf "$(SKILL_DIR)"
	mkdir -p "$(SKILL_DIR)" "$(BIN_DIR)"
	cp lurk skill/SKILL.md $(UPDATE_SH) "$(SKILL_DIR)/"
	rm -f "$(BIN_DIR)/lurk"
	cp lurk "$(BIN_DIR)/lurk"
	@echo "Installed lurk $(VERSION) -> $(SKILL_DIR) and $(BIN_DIR)/lurk"

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
