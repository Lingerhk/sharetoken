# What building the desktop app for Linux takes, and makensis for the Windows
# installer. Ubuntu 22.04, so the program runs on that release and on newer ones:
# it links this release's glibc, GTK 4 and WebKitGTK 6.0 (Wails v3).
FROM ubuntu:22.04
ARG GO_VERSION=go1.27.3
RUN apt-get update -qq \
 && apt-get install -y -qq --no-install-recommends ca-certificates curl build-essential pkg-config libgtk-4-dev libwebkitgtk-6.0-dev nsis perl >/dev/null \
 && arch=$(dpkg --print-architecture) \
 && curl -fsSL "https://go.dev/dl/${GO_VERSION}.linux-${arch}.tar.gz" | tar -xz -C /usr/local
ENV PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
