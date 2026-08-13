FROM plexinc/pms-docker:1.43.0.10492-121068a07

RUN apt-get update \
    && apt-get install -y --no-install-recommends netbase nfs-common \
    && rm -rf /var/lib/apt/lists/*
