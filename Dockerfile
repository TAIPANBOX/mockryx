# The drill runner as a published image.
#
# Until now every deployment BUILT this from source on the machine it was
# installing to, which cost minutes on a first run and put the whole Go
# toolchain in the blast radius of an install. What nobody builds, nobody
# breaks.
#
# Static, distroless, non-root, multi-arch. CGO off is what makes the binary
# runnable on distroless static AND what makes cross-compiling to arm64 free:
# there is no C toolchain to arrange, so the arm64 image costs the same as the
# amd64 one.
#
# The binary lands at /usr/local/bin/service and is the ENTRYPOINT, which is
# not cosmetic: the stack's Kubernetes manifests pass `args` and rely on that
# path, so this image is a drop-in replacement for the one they used to build
# on the node.
ARG GO_VERSION=1.26

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
ENV GOTOOLCHAIN=auto
WORKDIR /src
# Dependencies first, so a code-only change does not re-download the module
# graph on every build.
COPY go.mod go.su[m] ./
RUN go mod download
COPY . .
# TARGETARCH comes from buildx, one value per platform being built. Building
# FROM the build platform and cross-compiling, rather than emulating the target
# under QEMU, is the difference between a minute and a quarter of an hour.
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/service ./cmd/mockryx

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.title="mockryx"
LABEL org.opencontainers.image.description="Rehearses hostile scenarios against your own gateway and confirms every guardrail holds before production."
LABEL org.opencontainers.image.source="https://github.com/TAIPANBOX/mockryx"
LABEL org.opencontainers.image.licenses="Apache-2.0"
# Event logs and any per-service store are mounted, never baked.
VOLUME ["/var/lib/stack"]
COPY --from=build /out/service /usr/local/bin/service
# The scenarios ship WITH the tool. `mockryx run` requires a scenario
# directory, and an image without one is a binary that cannot do the only thing
# it exists for: measured on a live cluster 2026-08-03, where the stack's
# weekly drill CronJob had never once run, failing every time with "run
# requires exactly one scenario directory".
#
# Twenty kilobytes of YAML, in an image that is three megabytes. An operator
# who wants their own set still passes a path and a mount; this is the default,
# not a lock.
COPY scenarios/ /scenarios/
# 65532 is distroless's `nonroot` uid. Numeric on purpose: a kubelet with
# runAsNonRoot cannot verify a NAME and refuses the container outright.
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/service"]
