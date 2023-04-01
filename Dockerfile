FROM golang:1.20.2 AS build

ADD . /src

WORKDIR /src
RUN go build -v
RUN ls -la

FROM chromedp/headless-shell:latest
RUN apt update; apt install dumb-init

WORKDIR /app
COPY --from=build /src/screensnap /app/screensnap


ENTRYPOINT ["dumb-init", "--"]
CMD ["/app/screensnap"]
