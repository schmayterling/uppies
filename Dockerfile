# Build stage: compiles one static binary. No C toolchain is needed.
FROM golang:1.27-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/uppies ./cmd/uppies \
    && mkdir /out/data

# Final stage: the binary, CA certificates and an unprivileged user. Nothing else.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/uppies /uppies
COPY --from=build --chown=nonroot:nonroot /out/data /data
USER nonroot
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/uppies"]
CMD ["-addr", ":8080", "-db", "/data/uppies.db"]
