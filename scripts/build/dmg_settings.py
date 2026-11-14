# dmgbuild settings for the macOS disk image (scripts/build/build-gui.sh). dmgbuild
# writes the window layout (.DS_Store) itself and mounts the image hidden
# (-nobrowse), so building never opens Finder windows on the desktop.
#
# Defines (dmgbuild -D): app, the .app to put in; background, the TIFF.
# The window is 660x420 points; the 160-point icons sit where the arrow of
# gui/packaging/gen_dmg_background.go points from and to.
import os.path

app = defines["app"]  # noqa: F821 (dmgbuild provides defines)
name = os.path.basename(app)

format = "UDZO"
compression_level = 9
filesystem = "HFS+"
files = [app]
symlinks = {"Applications": "/Applications"}
background = defines["background"]  # noqa: F821
show_status_bar = False
show_tab_view = False
show_toolbar = False
show_pathbar = False
show_sidebar = False
window_rect = ((200, 120), (660, 420))
default_view = "icon-view"
arrange_by = None
icon_size = 160
text_size = 13
icon_locations = {name: (165, 200), "Applications": (495, 200)}
