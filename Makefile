.PHONY: frontend backend build check test verify

# ---- Building ---------------------------------------------------------------

frontend:
	cd frontend && pnpm install && pnpm build

backend: frontend
	go build -o calnode ./cmd/calnode

build: backend

# ---- Quality checks ---------------------------------------------------------
# Three levels, each a superset of the one before. They run the same commands as
# .github/workflows/ci.yml; when a step changes there, change it here too.
#
#   make check    Formatting, go vet, frontend types. Run while working.
#   make test     + every automated test that needs no browser.
#   make verify   + a fresh frontend build and the real-browser visual tests.
#                 This is what CI runs. Run it before opening a pull request.

JS_TESTS := internal/handler/assets/booking-logic.test.js \
            internal/handler/assets/room-logic.test.js \
            internal/handler/assets/bookmeet-skin.test.cjs

# The Go binary embeds frontend/build, so nothing compiles (vet included) without it.
frontend/build:
	$(MAKE) frontend

check: frontend/build
	@fmtout=$$(gofmt -l .); if [ -n "$$fmtout" ]; then \
		echo "Not gofmt'd (run: gofmt -w .):"; echo "$$fmtout"; exit 1; fi
	go vet ./...
	cd frontend && pnpm check

test: check
	node --test $(JS_TESTS)
	go test ./...

verify:
	cd frontend && pnpm install --frozen-lockfile && pnpm build
	$(MAKE) test
	cd frontend && pnpm test:visual
	go build ./...
