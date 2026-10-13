FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build

ARG TARGETOS
ARG TARGETARCH
ARG GIT_REVISION=HEAD
ARG BUILD_STAMP=unknown
# Set to "watch" to reload quota automatically when files change.
ARG BUILD_TAGS=

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY config ./config
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -tags "${BUILD_TAGS}" \
    -ldflags "-s -w -X main.gitRevision=${GIT_REVISION} -X main.buildStamp=${BUILD_STAMP}" \
    -o /out/ggr .

FROM alpine:3

RUN apk add --no-cache tzdata ca-certificates
COPY --from=build /out/ggr /usr/bin/ggr

USER 65534:65534
EXPOSE 4444
ENTRYPOINT ["/usr/bin/ggr", "-listen", ":4444", "-users", "/etc/grid-router/users.htpasswd", "-quotaDir", "/etc/grid-router/quota"]
