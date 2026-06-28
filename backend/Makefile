GO ?= go
COVER_MIN ?= 85

.PHONY: build vet test test-race cover integration docker-build clean

build:
	$(GO) build ./...

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

test-race:
	$(GO) test ./... -race

# Coverage over the library packages (cmd/ mains are thin and covered by the
# docker integration test). Fails if below COVER_MIN.
cover:
	$(GO) test ./internal/... -coverprofile=coverage.out -covermode=atomic
	@$(GO) tool cover -func=coverage.out | tail -1
	@pct=$$($(GO) tool cover -func=coverage.out | tail -1 | awk '{print $$3}' | tr -d '%'); \
	  awk "BEGIN{ if ($$pct+0 < $(COVER_MIN)) { printf \"coverage %.1f%% below min $(COVER_MIN)%%\\n\", $$pct; exit 1 } else { printf \"coverage %.1f%% OK (min $(COVER_MIN)%%)\\n\", $$pct } }"

# End-to-end join flow across real containers (needs Docker).
integration:
	$(GO) test ./deploy/... -tags=integration -v -timeout=300s

docker-build:
	docker compose -f deploy/compose.yml build

clean:
	rm -f coverage.out cov-internal.out
	$(GO) clean
