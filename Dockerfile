FROM alpine:3.24.4@sha256:93b41ef40b1677fce8fcc64ff4af09daddfacf5040afde618bc46c1e2d1cc7d4

RUN apk add --no-cache ca-certificates git \
    && git config --system --add safe.directory '*'

ENV XDG_CACHE_HOME=/tmp/.cache

ARG TARGETPLATFORM
COPY $TARGETPLATFORM/deadvalues /usr/local/bin/deadvalues

USER 65532:65532
WORKDIR /repo
ENTRYPOINT ["deadvalues"]