# compose-app's image: the fixture's server, built in the golang image and
# copied into a minimal runtime that carries nothing but the binary — no
# curl, no wget, no shell. The compose healthcheck calls the binary's own
# -healthcheck probe (an HTTP GET against /healthz) instead of shelling
# out, so the check works in whatever image this builds.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 go build -o /server ./cmd/server

FROM scratch
COPY --from=build /server /server
EXPOSE 8080
ENTRYPOINT ["/server"]
