; The Windows installer of the desktop app, built by scripts/build/build-gui.sh. It
; installs for the current user: no administrator rights, nothing outside the
; user's profile. The account and its keys are in the data directory, which
; neither an upgrade nor an uninstall touches.
Unicode true
SetCompressor /SOLID lzma
RequestExecutionLevel user
!include "MUI2.nsh"
!include "FileFunc.nsh"

Name "${NAME}"
OutFile "${OUTFILE}"
InstallDir "$LOCALAPPDATA\Programs\ShareToken${SUFFIX}"
InstallDirRegKey HKCU "Software\ShareToken${SUFFIX}" "InstallDir"
!define UNINSTKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\ShareToken${SUFFIX}"

VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName" "${NAME}"
VIAddVersionKey "FileDescription" "${NAME} installer"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "LegalCopyright" "ShareToken"

!define MUI_ICON "${SRC}\icon.ico"
!define MUI_UNICON "${SRC}\icon.ico"
!define MUI_FINISHPAGE_RUN "$INSTDIR\${GUI}.exe"
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"
!insertmacro MUI_LANGUAGE "SimpChinese"
!insertmacro MUI_LANGUAGE "TradChinese"
!insertmacro MUI_LANGUAGE "Japanese"
!insertmacro MUI_LANGUAGE "Korean"
!insertmacro MUI_LANGUAGE "German"
!insertmacro MUI_LANGUAGE "French"
!insertmacro MUI_LANGUAGE "Spanish"
!insertmacro MUI_LANGUAGE "Russian"
!insertmacro MUI_LANGUAGE "PortugueseBR"

!macro StopApp ; a running program's file cannot be replaced
  nsExec::Exec 'taskkill /F /T /IM "${GUI}.exe"'
  Pop $0
  nsExec::Exec 'taskkill /F /T /IM "${EXE}.exe"'
  Pop $0
  Sleep 500
!macroend
!macro UserPath ACTION
  nsExec::Exec 'powershell -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "$INSTDIR\user-path.ps1" -Action ${ACTION} -Dir "$INSTDIR"'
  Pop $0
  SendMessage ${HWND_BROADCAST} ${WM_WININICHANGE} 0 "STR:Environment" /TIMEOUT=500
!macroend

Section
  !insertmacro StopApp
  SetOutPath "$INSTDIR"
  File "${SRC}\${EXE}.exe"
  File "${SRC}\${GUI}.exe"
  File "${SRC}\icon.ico"
  File "${SRC}\user-path.ps1"
  WriteUninstaller "$INSTDIR\uninstall.exe"
  CreateShortcut "$SMPROGRAMS\${NAME}.lnk" "$INSTDIR\${GUI}.exe"
  CreateShortcut "$DESKTOP\${NAME}.lnk" "$INSTDIR\${GUI}.exe"
  !insertmacro UserPath add
  WriteRegStr HKCU "Software\ShareToken${SUFFIX}" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayName" "${NAME}"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINSTKEY}" "Publisher" "ShareToken"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayIcon" "$INSTDIR\icon.ico"
  WriteRegStr HKCU "${UNINSTKEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTKEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr HKCU "${UNINSTKEY}" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /S'
  WriteRegDWORD HKCU "${UNINSTKEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTKEY}" "NoRepair" 1
  ; invite links from the portal's pages: sharetoken://p/<token> (gui/invite.go)
  SetRegView 64
  WriteRegStr HKCU "Software\Classes\sharetoken" "" "URL:ShareToken"
  WriteRegStr HKCU "Software\Classes\sharetoken" "URL Protocol" ""
  WriteRegStr HKCU "Software\Classes\sharetoken\DefaultIcon" "" "$INSTDIR\icon.ico"
  WriteRegStr HKCU "Software\Classes\sharetoken\shell\open\command" "" '"$INSTDIR\${GUI}.exe" "%1"'
  SetRegView default
  ; /RELAUNCH: the app's one-click update runs this silently (/S) and wants the
  ; app back afterwards (gui/update_windows.go).
  ${GetParameters} $R0
  ClearErrors
  ${GetOptions} $R0 "/RELAUNCH" $R1
  IfErrors +2
  Exec '"$INSTDIR\${GUI}.exe"'
SectionEnd

Section "Uninstall"
  !insertmacro StopApp
  !insertmacro UserPath remove
  Delete "$SMPROGRAMS\${NAME}.lnk"
  Delete "$DESKTOP\${NAME}.lnk"
  Delete "$INSTDIR\${EXE}.exe"
  Delete "$INSTDIR\${GUI}.exe"
  Delete "$INSTDIR\icon.ico"
  Delete "$INSTDIR\user-path.ps1"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"
  DeleteRegKey HKCU "${UNINSTKEY}"
  DeleteRegKey HKCU "Software\ShareToken${SUFFIX}"
  ; what the system notifications registered at each start (Wails v3: the toast
  ; activator, under the app's name, and its notification categories)
  ; This program is 32-bit, and Windows shows a 32-bit program its own copy of
  ; HKCU\Software\Classes\CLSID: the app wrote the 64-bit (native) one.
  SetRegView 64
  ; the sharetoken:// links, when they still open this copy
  ReadRegStr $1 HKCU "Software\Classes\sharetoken\shell\open\command" ""
  StrCmp $1 '"$INSTDIR\${GUI}.exe" "%1"' 0 +2
  DeleteRegKey HKCU "Software\Classes\sharetoken"
  ReadRegStr $0 HKCU "Software\Classes\AppUserModelId\${NAME}" "CustomActivator"
  StrCmp $0 "" +2
  DeleteRegKey HKCU "Software\Classes\CLSID\$0"
  DeleteRegKey HKCU "Software\Classes\AppUserModelId\${NAME}"
  DeleteRegKey HKCU "Software\${NAME}\NotificationCategories"
  DeleteRegKey /ifempty HKCU "Software\${NAME}"
  SetRegView default
SectionEnd
