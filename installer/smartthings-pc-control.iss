; Inno Setup script for SmartThings PC Control (#166). Build it with
; scripts\build-windows.ps1, which builds dist\smartthings-pc-control.exe and
; passes the version defines below.
;
; The setup only copies files. Everything else stays in the exe, exactly as
; the desktop app's [설치] button does it: `install` registers and starts the
; service, adds the firewall rules and locks the folder to administrators;
; `uninstall` undoes it. The exe's own uninstall keeps config.json and the
; logs, and so does this one unless the user asks otherwise.
;
; The app finds this installation through the uninstall key of AppId
; (gui/installsource.go) and then updates by running the next release's
; setup silently with /RELAUNCH=1 (gui/installerupdate.go). Never change
; AppId.

#ifndef AppVersion
  #define AppVersion "0.0.0-dev"
#endif
#ifndef AppVersionNumeric
  #define AppVersionNumeric "0.0.0.0"
#endif
#ifndef DistDir
  #define DistDir "..\dist"
#endif

#define AppName "SmartThings PC Control"
#define AppExe "smartthings-pc-control.exe"
#define AppUserModelID "Protomothis.SmartThingsPCControl"
#define ServiceName "RemoteShutdownService"
#define RunValue "SmartThingsPCControl"
#define RepoURL "https://github.com/Protomothis/smartthings-pc-control"

[Setup]
AppId={{C936EE42-5942-4E0F-A9D8-FE2E3D01C480}
AppName={#AppName}
AppVersion={#AppVersion}
AppVerName={#AppName} {#AppVersion}
AppPublisher=Protomothis
AppPublisherURL={#RepoURL}
AppSupportURL={#RepoURL}/issues
AppUpdatesURL={#RepoURL}/releases
VersionInfoVersion={#AppVersionNumeric}
VersionInfoProductVersion={#AppVersionNumeric}
VersionInfoDescription={#AppName} Setup
; All users only: the Windows service needs administrator rights anyway.
PrivilegesRequired=admin
; A copy installed with the desktop app's [설치] button is upgraded in place:
; the folder its service runs from when that holds our exe (the settings
; live next to it). Otherwise the app's recommended folder,
; {autopf}\{#AppName}. Later setups reuse the folder chosen first
; (UsePreviousAppDir), and the folder page shows on the first install only.
DefaultDirName={code:DefaultInstallDir}
DisableDirPage=auto
DisableProgramGroupPage=yes
DisableReadyPage=no
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0
OutputDir={#DistDir}
OutputBaseFilename=SmartThingsPCControl-Setup-{#AppVersion}
SetupIconFile=..\icon.ico
UninstallDisplayIcon={app}\{#AppExe}
UninstallDisplayName={#AppName}
WizardStyle=modern
Compression=lzma2/ultra64
SolidCompression=yes
; PrepareToInstall stops the service and every tray first; Restart Manager
; is the second line for anything else still holding the exe.
CloseApplications=force
RestartApplications=no
ShowLanguageDialog=auto
SetupLogging=yes

[Languages]
Name: "korean"; MessagesFile: "compiler:Languages\Korean.isl"; InfoBeforeFile: "privacy.ko.txt"
Name: "english"; MessagesFile: "compiler:Default.isl"; InfoBeforeFile: "privacy.en.txt"

[CustomMessages]
korean.LaunchApp=SmartThings PC Control 실행
korean.StoppingService=서비스와 트레이 앱을 멈추는 중이에요…
korean.InstallingService=서비스를 등록하고 시작하는 중이에요…
korean.InstallFailed=서비스를 등록하지 못했어요 (종료 코드 %1).%n%n파일은 설치됐어요. 앱을 실행해 설정 → 서비스 관리 → [설치]를 다시 눌러 보세요.
korean.MovedConfig=예전 위치(%1)의 설정 파일을 새 설치 폴더로 옮겨 왔어요.
korean.RemoveData=설정(config.json: 시크릿 · 텔레그램 토큰 포함), 상태 파일, 로그도 지울까요?%n%n[아니요]를 고르면 남겨 두고, 다시 설치하면 그대로 이어서 씁니다.%n%n폴더: %1
english.LaunchApp=Launch SmartThings PC Control
english.StoppingService=Stopping the service and the tray app…
english.InstallingService=Registering and starting the service…
english.InstallFailed=The service could not be registered (exit code %1).%n%nThe files are installed. Open the app and try Settings → Service Management → [Install] again.
english.MovedConfig=The settings from the previous location (%1) were copied to the new install folder.
english.RemoveData=Also delete the settings (config.json, including the secret and the Telegram token), the state file and the logs?%n%nChoose No to keep them; a later install picks them up again.%n%nFolder: %1

[Files]
Source: "{#DistDir}\{#AppExe}"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\LICENSE"; DestDir: "{app}"; DestName: "LICENSE.txt"; Flags: ignoreversion

[Icons]
; The very shortcut the app would make for itself (internal/appid): same
; target, `gui`, working folder and AUMID, which lets toasts show as
; banners. Seeing it in the all-users Start menu, the app makes no per-user
; copy and removes one an earlier [설치] left (no duplicate in Start).
Name: "{autoprograms}\{#AppName}"; Filename: "{app}\{#AppExe}"; Parameters: "gui"; WorkingDir: "{app}"; AppUserModelID: "{#AppUserModelID}"

[Run]
; The wizard's last page. Started as the user who ran the setup, not
; elevated (the tray writes the user's own autostart entry).
Filename: "{app}\{#AppExe}"; Parameters: "gui"; Description: "{cm:LaunchApp}"; Flags: nowait postinstall skipifsilent runasoriginaluser

[UninstallDelete]
; Leftovers of updates: the verified setup and its log (update\), the exe
; kept for rollback by an exe-swap update.
Type: filesandordirs; Name: "{app}\update"
Type: files; Name: "{app}\{#AppExe}.old"

[Code]
const
  SC_MANAGER_CONNECT = $0001;
  SERVICE_QUERY_STATUS = $0004;
  SERVICE_STOP = $0020;
  SERVICE_CONTROL_STOP = 1;
  SERVICE_STOPPED = 1;

type
  TServiceStatus = record
    dwServiceType: Cardinal;
    dwCurrentState: Cardinal;
    dwControlsAccepted: Cardinal;
    dwWin32ExitCode: Cardinal;
    dwServiceSpecificExitCode: Cardinal;
    dwCheckPoint: Cardinal;
    dwWaitHint: Cardinal;
  end;

function OpenSCManager(lpMachineName, lpDatabaseName: String; dwDesiredAccess: Cardinal): THandle;
  external 'OpenSCManagerW@advapi32.dll stdcall';
function OpenService(hSCManager: THandle; lpServiceName: String; dwDesiredAccess: Cardinal): THandle;
  external 'OpenServiceW@advapi32.dll stdcall';
function CloseServiceHandle(hSCObject: THandle): Boolean;
  external 'CloseServiceHandle@advapi32.dll stdcall';
function ControlService(hService: THandle; dwControl: Cardinal; var lpServiceStatus: TServiceStatus): Boolean;
  external 'ControlService@advapi32.dll stdcall';
function QueryServiceStatus(hService: THandle; var lpServiceStatus: TServiceStatus): Boolean;
  external 'QueryServiceStatus@advapi32.dll stdcall';

// Relaunch: the app's own updater ran this setup silently with /RELAUNCH=1
// (gui/installerupdate.go) and has quit; start the tray again afterwards.
function Relaunch: Boolean;
begin
  Result := WizardSilent and (ExpandConstant('{param:RELAUNCH|0}') = '1');
end;

// StopAppService stops the Windows service, whichever folder it runs from,
// and waits up to 30 s for it to report STOPPED. The service runs the very
// exe this setup replaces. Missing service: nothing to do.
procedure StopAppService;
var
  Manager, Service: THandle;
  Status: TServiceStatus;
  I: Integer;
begin
  Manager := OpenSCManager('', '', SC_MANAGER_CONNECT);
  if Manager = 0 then
    Exit;
  try
    Service := OpenService(Manager, '{#ServiceName}', SERVICE_STOP or SERVICE_QUERY_STATUS);
    if Service = 0 then
      Exit;
    try
      if QueryServiceStatus(Service, Status) and (Status.dwCurrentState <> SERVICE_STOPPED) then
      begin
        Log('Stopping service {#ServiceName}');
        ControlService(Service, SERVICE_CONTROL_STOP, Status);
        for I := 1 to 60 do
        begin
          if not QueryServiceStatus(Service, Status) or (Status.dwCurrentState = SERVICE_STOPPED) then
            Break;
          Sleep(500);
        end;
        Log(Format('Service state after stop: %d', [Status.dwCurrentState]));
      end;
    finally
      CloseServiceHandle(Service);
    end;
  finally
    CloseServiceHandle(Manager);
  end;
end;

// StopTrays ends every running copy of the exe: the tray of each signed-in
// user and anything the service had started. They hold the exe open, and a
// tray does not answer Restart Manager's close request reliably.
procedure StopTrays;
var
  ResultCode: Integer;
begin
  Exec(ExpandConstant('{sys}\taskkill.exe'), '/F /T /IM {#AppExe}', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  Log(Format('taskkill {#AppExe}: %d', [ResultCode]));
end;

procedure StopEverything;
begin
  StopAppService;
  StopTrays;
  // Let Windows release the image files before they are replaced.
  Sleep(500);
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
begin
  WizardForm.PreparingLabel.Caption := CustomMessage('StoppingService');
  StopEverything;
  Result := '';
end;

// ServiceExeDir is the folder the installed service runs from (its
// ImagePath), '' when there is none.
function ServiceExeDir: String;
var
  ImagePath: String;
begin
  Result := '';
  if RegQueryStringValue(HKLM, 'SYSTEM\CurrentControlSet\Services\{#ServiceName}', 'ImagePath', ImagePath) then
    Result := ExtractFileDir(RemoveQuotes(Trim(ImagePath)));
end;

// ExistingInstallDir is the folder of a copy installed with [설치]: the
// service's ImagePath names our exe and that exe is still there. '' when
// there is no such copy.
function ExistingInstallDir: String;
var
  ImagePath, Exe: String;
begin
  Result := '';
  if not RegQueryStringValue(HKLM, 'SYSTEM\CurrentControlSet\Services\{#ServiceName}', 'ImagePath', ImagePath) then
    Exit;
  Exe := RemoveQuotes(Trim(ImagePath));
  if (CompareText(ExtractFileName(Exe), '{#AppExe}') = 0) and FileExists(Exe) then
    Result := ExtractFileDir(Exe);
end;

// DefaultInstallDir is DefaultDirName: upgrade a [설치] copy where it is,
// else the recommended folder.
function DefaultInstallDir(Param: String): String;
begin
  Result := ExistingInstallDir;
  if Result = '' then
    Result := ExpandConstant('{autopf}\{#AppName}');
  Log('Default install folder: ' + Result);
end;

// CarryOverConfig copies config.json and state.json from a service that so
// far ran from another folder into {app} - only when the user picked a
// different folder on the folder page than the one the [설치] copy is in
// (the default upgrades it in place) - unless {app} has its own: the
// settings live next to the exe, and `install` would otherwise start the
// moved service with defaults.
procedure CarryOverConfig;
var
  OldDir, NewDir: String;
begin
  OldDir := ServiceExeDir;
  NewDir := ExpandConstant('{app}');
  if (OldDir = '') or (CompareText(AddBackslash(OldDir), AddBackslash(NewDir)) = 0) then
    Exit;
  if not FileExists(AddBackslash(OldDir) + 'config.json') or FileExists(AddBackslash(NewDir) + 'config.json') then
    Exit;
  if FileCopy(AddBackslash(OldDir) + 'config.json', AddBackslash(NewDir) + 'config.json', True) then
  begin
    FileCopy(AddBackslash(OldDir) + 'state.json', AddBackslash(NewDir) + 'state.json', True);
    Log('Copied config.json from ' + OldDir);
    if not WizardSilent then
      MsgBox(FmtMessage(CustomMessage('MovedConfig'), [OldDir]), mbInformation, MB_OK);
  end;
end;

// RegisterService runs the exe's own `install`, as the [설치] button does:
// service, firewall rules, folder permissions, start.
procedure RegisterService;
var
  ResultCode: Integer;
begin
  WizardForm.StatusLabel.Caption := CustomMessage('InstallingService');
  if not Exec(ExpandConstant('{app}\{#AppExe}'), 'install', ExpandConstant('{app}'), SW_HIDE, ewWaitUntilTerminated, ResultCode) then
    ResultCode := -1;
  Log(Format('{#AppExe} install: %d', [ResultCode]));
  if ResultCode <> 0 then
    SuppressibleMsgBox(FmtMessage(CustomMessage('InstallFailed'), [IntToStr(ResultCode)]), mbError, MB_OK, IDOK);
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  ResultCode: Integer;
begin
  if CurStep = ssPostInstall then
  begin
    CarryOverConfig;
    RegisterService;
    // The updater's run: this setup is elevated (started by the elevated
    // update-install), so the exe starts the tray as the desktop user.
    if Relaunch then
      Exec(ExpandConstant('{app}\{#AppExe}'), 'relaunch-gui', ExpandConstant('{app}'), SW_HIDE, ewNoWait, ResultCode);
  end;
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  ResultCode: Integer;
  Dir: String;
begin
  if CurUninstallStep = usUninstall then
  begin
    // Stop first so `uninstall` deletes a stopped service and the files
    // below are free; it then removes the service and the firewall rules.
    StopAppService;
    Exec(ExpandConstant('{app}\{#AppExe}'), 'uninstall', ExpandConstant('{app}'), SW_HIDE, ewWaitUntilTerminated, ResultCode);
    Log(Format('{#AppExe} uninstall: %d', [ResultCode]));
    StopTrays;
    Sleep(500);
    // This user's autostart entry and toast-button protocol (the tray
    // writes them per user; other users' entries point at a missing exe
    // and do nothing).
    RegDeleteValue(HKCU, 'Software\Microsoft\Windows\CurrentVersion\Run', '{#RunValue}');
    RegDeleteKeyIncludingSubkeys(HKCU, 'Software\Classes\stpc');
  end;
  if CurUninstallStep = usPostUninstall then
  begin
    // Like the exe's own uninstall, the settings and logs stay unless the
    // user asks; a silent uninstall always keeps them.
    Dir := ExpandConstant('{app}');
    if DirExists(Dir) and not UninstallSilent and
       (MsgBox(FmtMessage(CustomMessage('RemoveData'), [Dir]), mbConfirmation, MB_YESNO or MB_DEFBUTTON2) = IDYES) then
    begin
      DelTree(Dir, True, True, True);
      DelTree(ExpandConstant('{localappdata}\{#AppName}'), True, True, True);
      DelTree(ExpandConstant('{userappdata}\fyne\com.protomothis.smartthings-pc-control'), True, True, True);
    end;
  end;
end;
