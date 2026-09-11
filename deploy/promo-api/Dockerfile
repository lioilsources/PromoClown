# Runtime image for JODA. deploy.sh cross-compiles the binary on the machine
# it runs on (CGO_ENABLED=0, linux/amd64), so JODA — 3.8 GB RAM, swap full —
# never runs a Go build. The repo-root Dockerfile builds from source instead.
FROM gcr.io/distroless/static-debian12:nonroot
COPY bin/promo-api /usr/local/bin/promo-api
EXPOSE 8094
VOLUME /data
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["/usr/local/bin/promo-api", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/promo-api"]
CMD ["serve"]
