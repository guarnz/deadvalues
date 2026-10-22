FROM alpine:3.24.3@sha256:b3413572228e34bd1c9245f5565e6cd127005655e8a7899aef33d8e83255c9e6

RUN apk add --no-cache ca-certificates git \
    && git config --system --add safe.directory '*'

ENV XDG_CACHE_HOME=/tmp/.cache

ARG TARGETPLATFORM
COPY $TARGETPLATFORM/deadvalues /usr/local/bin/deadvalues

USER 65532:65532
WORKDIR /repo
ENTRYPOINT ["deadvalues"]