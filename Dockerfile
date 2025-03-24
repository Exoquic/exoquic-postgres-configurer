FROM golang:1.20-alpine AS builder
WORKDIR /app
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o exoquic-configurer .
FROM alpine:3.18
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /app/exoquic-configurer .
RUN chmod +x /app/exoquic-configurer
ENV PGHOST=""
ENV PGPORT="5432"
ENV PGUSER=""
ENV PGPASSWORD=""
ENV PGDATABASE=""
ENV EXOQUIC_REPLICATION_USER="exoquic_replication"
ENV EXOQUIC_REPLICATION_PASSWORD=""
ENV EXOQUIC_PUBLICATION_NAME="exoquic_publication"
ENV EXOQUIC_SLOT_NAME="exoquic_replication_slot"
ENV EXOQUIC_API_KEY=""
ENV EXOQUIC_CLOUD_URL="https://api.exoquic.com"
ENV TABLES_TO_CAPTURE=""

CMD ["/app/exoquic-configurer"]
