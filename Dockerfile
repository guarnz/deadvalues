FROM alpine:3.24.3@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a

RUN apk add --no-cache ca-certificates git \
    && git config --system --add safe.directory '*'

ENV XDG_CACHE_HOME=/tmp/.cache

ARG TARGETPLATFORM
COPY $TARGETPLATFORM/deadvalues /usr/local/bin/deadvalues

USER 65532:65532
WORKDIR /repo
ENTRYPOINT ["deadvalues"]