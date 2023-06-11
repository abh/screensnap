FROM golang:1.20.5 AS build

ADD . /src

WORKDIR /src

# bake dependencies into layer to avoid redownoading if they haven't changed.
COPY go.mod .
COPY go.sum .

RUN --mount=type=cache,id=gomod,target=/go/pkg/mod \
    go mod download

RUN --mount=type=cache,id=gomod,target=/go/pkg/mod \
    --mount=type=cache,id=gobuild,target=/root/.cache/go-build \
    go build -v

RUN ls -la

FROM chromedp/headless-shell:latest
RUN apt update; apt install -y dumb-init procps

WORKDIR /app
COPY --from=build /src/screensnap /app/screensnap


ENTRYPOINT ["dumb-init", "--"]
CMD ["/app/screensnap"]
