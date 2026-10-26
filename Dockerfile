FROM ghcr.io/drakkan/sftpgo:v2.7.6-distroless-slim@sha256:483f4ff2e6b6c659b7ec5b4beaa11506665fde1a4528cdeb6b4f4b624c0fff6e AS sftpgo-donor

FROM golang:1.27.3-alpine3.24@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS go-builder

WORKDIR /build

COPY go.mod ./
COPY init/ ./init/
COPY probe/ ./probe/

COPY --from=sftpgo-donor /usr/share/sftpgo /rootfs/usr/share/sftpgo
COPY --from=sftpgo-donor /etc/mime.types /out/etc/mime.types

RUN mkdir -p /out/data /out/var/log/sftpgo /out/var/run/postgresql /out/etc/ssl/certs /out/usr/local/bin && \
    chmod 0755 /out/etc /out/etc/ssl /out/etc/ssl/certs && \
    chmod 0755 /out/var /out/var/log /out/var/log/sftpgo /out/var/run /out/var/run/postgresql && \
    chmod 0700 /out/data && \
    chmod 0444 /out/etc/mime.types && \
    cp /etc/ssl/certs/ca-certificates.crt /out/etc/ssl/certs/ca-certificates.crt && \
    chmod 0444 /out/etc/ssl/certs/ca-certificates.crt && \
    find /rootfs/usr/share/sftpgo -type d -exec chmod 0555 {} + && \
    find /rootfs/usr/share/sftpgo -type f -exec chmod 0444 {} + && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath -o /out/usr/local/bin/init ./init && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath -o /out/usr/local/bin/probe ./probe

FROM scratch AS server

COPY --from=go-builder /out/etc /etc
COPY --from=go-builder /out/var /var
COPY --from=go-builder --chmod=0700 /out/data /data
COPY --from=go-builder /rootfs/usr/share/sftpgo /usr/share/sftpgo
COPY --from=sftpgo-donor --chmod=0555 /usr/local/bin/sftpgo /usr/local/bin/sftpgo
COPY --from=go-builder --chmod=0555 /out/usr/local/bin/init /usr/local/bin/init
COPY --from=go-builder --chmod=0555 /out/usr/local/bin/probe /usr/local/bin/probe

ENV HOME=/tmp \
    SFTPGO_LOG_FILE_PATH="/var/log/sftpgo/sftpgo.log"

ENTRYPOINT ["/usr/local/bin/init"]
