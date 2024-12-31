FROM golang:1.23.4-bookworm AS build

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

# check debian version with
#   docker run --rm -ti --entrypoint /bin/bash chromedp/headless-shell:133.0.6905.0
FROM chromedp/headless-shell:133.0.6905.0

RUN apt-get clean; apt-get update; apt install -y dumb-init procps

WORKDIR /app
COPY --from=build /src/screensnap /app/screensnap

# in debian bookworm, use --comment instead of --gecos
RUN adduser --disabled-password --gecos "" chromedp

USER chromedp

ENTRYPOINT ["dumb-init", "--"]
CMD ["/app/screensnap"]
