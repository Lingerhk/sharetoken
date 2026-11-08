#!/bin/sh
# Installs the app for the current user, under ~/.local.
set -eu
cd "$(dirname "$0")"
ENV=@ENV@
BIN="$HOME/.local/bin"
mkdir -p "$BIN" "$HOME/.local/share/applications" "$HOME/.local/share/icons/hicolor/512x512/apps"
install -m 755 "sharetoken-$ENV" "sharetoken-gui-$ENV" "$BIN/"
install -m 644 icon.png "$HOME/.local/share/icons/hicolor/512x512/apps/sharetoken-$ENV.png"
sed "s|^Exec=.*|Exec=$BIN/sharetoken-gui-$ENV|" "sharetoken-$ENV.desktop" > "$HOME/.local/share/applications/sharetoken-$ENV.desktop"
echo "installed to $BIN; find sharetoken ($ENV) in your application menu"
