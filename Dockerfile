FROM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6

RUN apk add --no-cache ca-certificates git \
    && git config --system --add safe.directory '*'

ENV XDG_CACHE_HOME=/tmp/.cache

ARG TARGETPLATFORM
COPY $TARGETPLATFORM/deadvalues /usr/local/bin/deadvalues

USER 65532:65532
WORKDIR /repo
ENTRYPOINT ["deadvalues"]