FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends samba \
    && rm -rf /var/lib/apt/lists/*

COPY smb.conf /etc/samba/smb.conf

EXPOSE 445

ENTRYPOINT ["smbd", "--foreground", "--no-process-group", "--debug-stdout"]
