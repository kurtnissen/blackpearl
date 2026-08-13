FROM golang:1.24-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
RUN CGO_ENABLED=0 go build -trimpath -o /out/readat-nfs .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/readat-nfs /readat-nfs
EXPOSE 20492
ENTRYPOINT ["/readat-nfs"]
