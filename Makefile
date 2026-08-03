.PHONY: build test vet spike e2e-dry test-nonet check-gitignore install-launchagent clean

GOFLAGS ?=
BIN     := bin

## build: compile the postbode binary (no cgo — modernc.org/sqlite is pure Go)
build:
	CGO_ENABLED=0 go build $(GOFLAGS) -o $(BIN)/postbode ./cmd/postbode

## test: the standing quality gate (NF-12). check-gitignore runs FIRST — a leaked
## secret is not something we want to discover after a green test run.
test: check-gitignore
	go test ./...

## vet: static analysis, part of the NF-12 gate
vet:
	go vet ./...

## check-gitignore: fail if any F-63 pattern is present but NOT ignored.
## Guards the one-way door: this repo has no remote to force-push over.
check-gitignore:
	@leaked=$$(git status --porcelain --untracked-files=all 2>/dev/null \
	  | awk '{print $$2}' \
	  | grep -E '(^|/)(credentials\.json|token\.json|session\.token|\.env)$$|\.db(-wal|-shm)?$$|(^|/)spool/' \
	  || true); \
	if [ -n "$$leaked" ]; then \
	  echo "check-gitignore: FAIL — these match F-63 secret patterns but are not ignored:"; \
	  echo "$$leaked" | sed 's/^/  /'; \
	  echo "Fix .gitignore before committing."; \
	  exit 1; \
	fi; \
	echo "check-gitignore: ok"

## test-nonet: NF-09 gate — the suite must pass with no live API access.
## Enforced in-process by the dialer guard (AC-22), not by the environment.
test-nonet:
	POSTBODE_TEST_NO_NETWORK=1 go test ./...

## spike: PRD P0. TOUCHES PRODUCTION — a real upload into a real accountant's
## queue. Requires CF_TOKEN and credentials.json. DELETE cmd/spike AFTER P1 (F-07).
spike:
	go run ./cmd/spike

## e2e-dry: full pipeline against a fixture mailbox and fake upload server (NF-10)
e2e-dry:
	go test ./tests/e2e/... -run TestE2EDry -v

## install-launchagent: install and load the LaunchAgent (F-61)
install-launchagent: build
	install -d $(HOME)/Library/LaunchAgents
	install -m 0644 launchd/be.vhco.postbode.plist $(HOME)/Library/LaunchAgents/
	launchctl unload $(HOME)/Library/LaunchAgents/be.vhco.postbode.plist 2>/dev/null || true
	launchctl load $(HOME)/Library/LaunchAgents/be.vhco.postbode.plist

clean:
	rm -rf $(BIN)
