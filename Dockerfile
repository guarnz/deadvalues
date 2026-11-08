FROM alpine:3.24.4@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf

RUN apk add --no-cache ca-certificates git \
    && git config --system --add safe.directory '*'

ENV XDG_CACHE_HOME=/tmp/.cache

ARG TARGETPLATFORM
COPY $TARGETPLATFORM/deadvalues /usr/local/bin/deadvalues

USER 65532:65532
WORKDIR /repo
ENTRYPOINT ["deadvalues"]