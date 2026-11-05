# syntax=docker/dockerfile:1
# Build this multi-stage Dockerfile from the locked source tree; one process only.
FROM golang:1.26.10-bookworm AS go-sdk

FROM node:24-bookworm-slim AS web
COPY --from=go-sdk /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:${PATH}"
WORKDIR /src
RUN apt-get update && apt-get install -y --no-install-recommends python3 ca-certificates && rm -rf /var/lib/apt/lists/*
COPY . .
RUN python3 scripts/hydrate_upstreams.py && python3 scripts/project.py build-web

FROM go-sdk AS build
RUN apt-get update && apt-get install -y --no-install-recommends python3 ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY --from=web /src /src
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ENV CGO_ENABLED=0 GOCACHE=/src/.build/go-cache
RUN python3 scripts/project.py build --skip-web --target ${TARGETOS}/${TARGETARCH} \
 && mkdir /out && cp -a dist/${TARGETOS}-${TARGETARCH}-portable/. /out/

FROM debian:bookworm-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata \
 && rm -rf /var/lib/apt/lists/* && useradd --uid 10001 --create-home app \
 && mkdir /state && chown app:app /state
COPY --from=build /out /opt/cliproxyapi-plus
USER 10001:10001
WORKDIR /state
VOLUME ["/state"]
EXPOSE 8317
ENTRYPOINT ["/opt/cliproxyapi-plus/cliproxyapi-plus"]
CMD ["--config", "/state/config.yaml", "--data-dir", "/state/data", "--allow-remote"]
