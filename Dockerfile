FROM golang:1.23 AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /grumpysenior .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /grumpysenior /grumpysenior
ENV PORT=8787
EXPOSE 8787
ENTRYPOINT ["/grumpysenior", "serve", "--trust-proxy"]
