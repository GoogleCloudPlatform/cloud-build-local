FROM scratch

COPY gopath/bin/container-builder-local /container-builder-local

ENTRYPOINT ["/container-builder-local"]
