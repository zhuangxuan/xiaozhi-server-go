FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY car-relay ./car-relay
RUN CGO_ENABLED=1 go build -o /out/car-relay ./car-relay

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 65532 --create-home car
WORKDIR /data
COPY --from=build /out/car-relay /usr/local/bin/car-relay
USER car
EXPOSE 8090
ENTRYPOINT ["car-relay", "-addr", ":8090", "-db", "/data/car-relay.db"]
