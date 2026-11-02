FROM golang:1.27-alpine AS build

# SQLite is C, so this needs a C compiler. Statically linked against musl, so the binary runs
# in an empty image.
RUN apk add --no-cache gcc musl-dev ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=1 go build \
    -tags "sqlite_fts5 sqlite_omit_load_extension" \
    -trimpath \
    -ldflags '-s -w -linkmode external -extldflags "-static"' \
    -o /baatplassen ./cmd/baatplassen

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /baatplassen /baatplassen
EXPOSE 8000
CMD ["/baatplassen", "serve", "--http"]
