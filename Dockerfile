# syntax=docker/dockerfile:1
FROM alpine
# TARGETOS TARGETARCH already set by '--platform'
RUN apk update
RUN apk add --no-cache  curl bash tree
RUN mkdir -p /usr/local/share/ca-certificates/

ARG TARGETOS TARGETARCH 
COPY kubegems-${TARGETOS}-${TARGETARCH} /app/kubegems
COPY config /app/config
COPY plugins /app/plugins
COPY tools /app/tools
WORKDIR /app
ENTRYPOINT ["/app/kubegems"]
