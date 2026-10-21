#!/bin/bash
# Builds both installers into dist/:
#   DataRecovery.dmg        macOS (Apple Silicon + Intel), drag to Applications
#   DataRecovery-Setup.exe  Windows 10/11 (64-bit) installer
# Needs Go, the Wails CLI (go install github.com/wailsapp/wails/v2/cmd/wails@v2.10.1)
# and NSIS (brew install makensis).
set -euo pipefail
cd "$(dirname "$0")/.."
WAILS="${WAILS:-$(go env GOPATH)/bin/wails}"
rm -rf dist && mkdir dist

"$WAILS" build -platform darwin/universal -clean
codesign --force --deep --sign - build/bin/DataRecovery.app
STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT
cp -R build/bin/DataRecovery.app "$STAGE/"
cp README-mac.txt "$STAGE/README.txt"
ln -s /Applications "$STAGE/Applications"
hdiutil create -volname DataRecovery -srcfolder "$STAGE" -fs HFS+ -format UDZO -ov dist/DataRecovery.dmg >/dev/null

"$WAILS" build -platform windows/amd64 -nsis -skipbindings
cp build/bin/DataRecovery-amd64-installer.exe dist/DataRecovery-Setup.exe
cp README.txt dist/README-windows.txt
cp README-mac.txt dist/README-mac.txt
ls -lh dist
