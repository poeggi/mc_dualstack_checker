# SPDX-License-Identifier: AGPL-3.0-or-later
# The backend as an OCI image, pages included. The binary is cross-compiled
# on the build machine, so other architectures need no emulation:
#   podman build --platform linux/amd64,linux/arm64 --build-arg VERSION=v1.2.3 backend
# Run it with the host's network (the probes need its IPv6) and a writable
# /state for the usage numbers. See the README.
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1 AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
        -ldflags "-s -w -X main.version=$VERSION" -o /out/mc_dualstack_check . \
    && mkdir /out/state

FROM scratch
LABEL org.opencontainers.image.source=https://github.com/poeggi/mc_dualstack_checker \
      org.opencontainers.image.licenses=AGPL-3.0-or-later
COPY --from=build /out/mc_dualstack_check /mc_dualstack_check
COPY --from=build --chown=65532:65532 /out/state /state
ENV PORT=8080 STATE_DIRECTORY=/state
USER 65532:65532
ENTRYPOINT ["/mc_dualstack_check"]
