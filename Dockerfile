FROM golang:1.23 AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /grumpysenior .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /grumpysenior /grumpysenior
EXPOSE 8787
ENTRYPOINT ["/grumpysenior", "serve", "--addr", "0.0.0.0:8787", "--trust-proxy"]
