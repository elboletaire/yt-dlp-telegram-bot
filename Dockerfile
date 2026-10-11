FROM golang:1.25 AS builder
WORKDIR /app/
COPY go.mod go.sum /app/
RUN go mod download
COPY . .
ARG GOARCH=amd64
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$GOARCH go build -v

FROM python:alpine
RUN apk update && apk upgrade && apk add --no-cache ffmpeg
COPY --from=builder /app/yt-dlp-telegram-bot /app/yt-dlp-telegram-bot
COPY --from=builder /app/yt-dlp.conf /root/yt-dlp.conf

ENTRYPOINT ["/app/yt-dlp-telegram-bot"]
ENV API_ID= API_HASH= BOT_TOKEN= ALLOWED_USERIDS= ADMIN_USERIDS= ALLOWED_GROUPIDS= YTDLP_COOKIES= TROLL_USERIDS= TROLL_SENTENCES= TROLL_PROBABILITY= LIKES_ENABLED= DB_PATH= BEST_OF_INTERVAL= BEST_OF_AT= BEST_OF_COUNT= MESSAGES_FILE=