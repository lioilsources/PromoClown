VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  = -s -w -X main.version=$(VERSION)

# CLIs that run as OpenClaw skills on the DGX Spark (linux/arm64).
# promo-tribute and promo-restyle run on the Spark because ComfyUI does; the
# rest are the agent's skill CLIs.
SKILL_CMDS = promo store-reviews youtube-comments reddit-monitor bluesky-mentions \
             promo-tribute promo-restyle

JODA        ?= joda
SPARK       ?= spark
POSTIZ_HOST ?= spark

.PHONY: generate build test vet spark-bins deploy-promo-api deploy-postiz deploy-spark clean

# sqlc output is committed, so building never needs sqlc installed.
generate:
	sqlc generate

build:
	@mkdir -p bin
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o bin/ ./cmd/...

test:
	go test ./...

vet:
	go vet ./...

spark-bins:
	@mkdir -p bin/linux-arm64
	@for c in $(SKILL_CMDS); do \
		echo "build $$c (linux/arm64)"; \
		CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o bin/linux-arm64/$$c ./cmd/$$c || exit 1; \
	done

# promo-api + approval bot + publisher on JODA (binary cross-compiled here).
deploy-promo-api:
	./deploy/promo-api/deploy.sh $(JODA)

# Postiz + Temporal. Needs ~3 GB RAM, which JODA does not have.
deploy-postiz:
	./deploy/postiz/deploy.sh $(POSTIZ_HOST)

# Skill CLIs, SKILL.md files, workspace files and user units on the Spark.
deploy-spark: spark-bins
	./deploy/spark/deploy.sh $(SPARK)

clean:
	rm -rf bin
