#ifndef AppVersion
  #define AppVersion "0.1.0"
#endif
#ifndef ReleaseVersion
  #define ReleaseVersion "v0.1.0"
#endif
#define ProjectRoot AddBackslash(SourcePath) + "..\..\.."

[Setup]
AppId={{D9DC5147-865C-4ACD-9200-F8276F98AE2D}
AppName=LinkSphere
AppVersion={#AppVersion}
AppPublisher=LinkSphere Contributors
DefaultDirName={localappdata}\Programs\LinkSphere
DefaultGroupName=LinkSphere
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
OutputDir={#ProjectRoot}\dist
OutputBaseFilename=LinkSphere-Setup-{#ReleaseVersion}-windows-amd64
SetupIconFile={#ProjectRoot}\web\public\favicon.ico
UninstallDisplayIcon={app}\GOSSHD.exe
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
CloseApplications=yes
RestartApplications=no

[Tasks]
Name: "desktopicon"; Description: "Create a desktop shortcut"; Flags: unchecked

[Files]
Source: "{#ProjectRoot}\package\gosshd-client-win-x64\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs

[Icons]
Name: "{group}\LinkSphere"; Filename: "{app}\GOSSHD.exe"
Name: "{autodesktop}\LinkSphere"; Filename: "{app}\GOSSHD.exe"; Tasks: desktopicon

[Run]
Filename: "{app}\GOSSHD.exe"; Description: "Launch LinkSphere"; Flags: nowait postinstall skipifsilent
