FROM golang:1.20.3 AS build

ADD . /src

WORKDIR /src

# bake dependencies into layer to avoid redownoading if they haven't changed.
COPY go.mod .
COPY go.sum .
RUN go mod download

RUN go build -v
RUN ls -la

FROM chromedp/headless-shell:latest
RUN apt update; apt install dumb-init

WORKDIR /app
COPY --from=build /src/screensnap /app/screensnap


ENTRYPOINT ["dumb-init", "--"]
CMD ["/app/screensnap"]
