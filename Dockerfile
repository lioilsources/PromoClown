# promo-api for JODA (linux/amd64). Pure-Go SQLite, so a static binary on
# distroless is all it needs.
FROM golang:1.26-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/promo-api ./cmd/promo-api

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/promo-api /usr/local/bin/promo-api
EXPOSE 8094
VOLUME /data
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["/usr/local/bin/promo-api", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/promo-api"]
CMD ["serve"]
