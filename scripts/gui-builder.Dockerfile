# What building the desktop app for Linux takes, and makensis for the Windows
# installer. Ubuntu 22.04, so the program runs on that release and on newer ones:
# it links this release's glibc and WebKitGTK 4.1, the oldest to have that API.
FROM ubuntu:22.04
ARG GO_VERSION=go1.27.3
RUN apt-get update -qq \
 && apt-get install -y -qq --no-install-recommends ca-certificates curl build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev nsis perl >/dev/null \
 && arch=$(dpkg --print-architecture) \
 && curl -fsSL "https://go.dev/dl/${GO_VERSION}.linux-${arch}.tar.gz" | tar -xz -C /usr/local
ENV PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
