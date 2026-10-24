# Template Windows

Decyzje: ADR-0005 (zawsze sysprep), ADR-0007 (OpenSSH na Win11 z MSI). Szablony zbudowane w spike'u przez Packera jako bibliotekę (ADR-0004); `Autounattend.xml` i `afk-setup.ps1` przenoszą się na CLI bez zmian, ale **przez CLI jeszcze ich nie budowaliśmy**.

## Wejście

| Template | ISO | Indeks obrazu |
|---|---|---|
| `ws2025` | Server 2025 Eval (ws2025-eval.iso) | 2 — Standard (Desktop Experience) |
| `win11` | Win11 25H2 (np. `Win11_25H2_Polish_x64_v2.iso`) | 5 — Pro, nieaktywowany |
| `win11` (alternatywa) | Win11 Enterprise Eval | — |

ISO i SHA256 podaje użytkownik w `.afk.toml`. Dodatkowo (pobiera `afk`, sumy przypięte):

- `virtio-win.iso` 0.1.302 (`fedorapeople.org/.../stable-virtio/`), rozpakowany raz (`7z x`) i dołączany do płyty instalacyjnej.
- `virtio-win-guest-tools.exe` (agent QEMU, balloon).
- `OpenSSH-Win64-v10.4.0.0.msi` (ADR-0007).

Płyta `cd_label = "AFKSETUP"`: rozpakowany virtio-win + `Autounattend.xml` + `afk-setup.ps1` + MSI + `authorized_keys` (klucz publiczny `afk`).

## Builder

```
iso_checksum      = <z konfiguracji>
disk_size         = 64G, memory = 4096, cpus = 4
efi_boot          = true
efi_firmware_code = /usr/share/OVMF/OVMF_CODE_4M.fd
efi_firmware_vars = /usr/share/OVMF/OVMF_VARS_4M.fd
vtpm              = true (Win11)
cpu_model         = host
boot_wait         = 1s
boot_command      = ["<down><wait>" × 20]
communicator      = ssh, ssh_username = afk, ssh_timeout = 100m
shutdown_command  = shutdown /s /t 5 /f /d p:4:1 /c "afk"
shutdown_timeout  = 15m
```

`boot_command`: trzeba trafić w „Press any key to boot from CD” w UEFI. **Strzałka, nie spacja** — spacja trafiona już w instalatorze wciska „Anuluj”.

Win11 i Secure Boot: w spike'u obejście `HKLM\SYSTEM\Setup\LabConfig\BypassSecureBootCheck=1` w `windowsPE`. Wariant z kluczami MS w OVMF (`OVMF_VARS_4M.ms.fd`) niesprawdzony.

## Fazy instalacji

| Faza | Kto | Co |
|---|---|---|
| `windowsPE` | Setup | partycje GPT (EFI 260, MSR 16, NTFS), obraz z indeksu, sterowniki virtio z `DriverPaths` (viostor) |
| `specialize` | SYSTEM | `afk-setup.ps1 -Phase specialize`: konto `afk`, sterowniki virtio przez `pnputil` (NetKVM potrzebny do ssh), klucz, optymalizacje |
| `oobeSystem` | — | ukrycie ekranów OOBE, hasło Administratora, autologon `afk` (LogonCount 1) |
| `firstlogon` | `afk` | `afk-setup.ps1 -Phase firstlogon`: guest tools, OpenSSH (wbudowany lub MSI), firewall 22, plan zasilania, start sshd |

sshd startuje dopiero w `firstlogon` — inaczej Packer łączy się przed końcem OOBE i provisioning trafia w niegotowy system.

## Pułapki

- **MSI w `specialize` → 1603.** Instalatory MSI tylko w `firstlogon`; w `specialize` sterowniki przez `pnputil /add-driver ... /install`.
- **Lokalizacja.** Polskie ISO ma grupę „Administratorzy”. Członkostwo i ACL tylko po SID: `Add-LocalGroupMember -SID 'S-1-5-32-544'`, `icacls ... /grant '*S-1-5-32-544:F' /grant '*S-1-5-18:F'`.
- **Hasło** nie może zawierać nazwy konta (polityka złożoności) — inaczej konto nie powstaje.
- **Klucz admina**: `C:\ProgramData\ssh\administrators_authorized_keys` z ACL tylko Administratorzy + SYSTEM, inaczej sshd go ignoruje. `HKLM:\SOFTWARE\OpenSSH\DefaultShell` = `powershell.exe`.
- **Skrypty przez ssh**: wieloliniowy skrypt jako argument `powershell -c` się sypie → `powershell -NoProfile -NonInteractive -EncodedCommand <base64(UTF-16LE)>`. Na początku `$ProgressPreference = 'SilentlyContinue'`, inaczej stderr zalewa CLIXML.
- **Windows OpenSSH zabija procesy sesji** po jej zamknięciu. Długie operacje (sysprep, instalatory) synchronicznie w sesji albo przez zadanie harmonogramu.
- **Windows Update ignoruje proxy WinHTTP** (`0x8024402c`) — nic, co wymaga WU, nie zadziała w budowie (ADR-0007).
- Optymalizacje w obrazie: `NoAutoUpdate=1`, `AllowTelemetry=0`, wyłączone `wuauserv`, `UsoSvc`, `WSearch`, `SysMain`, `DiagTrack`, hibernacja, defrag; plan zasilania „wysoka wydajność”.

## Sysprep (ADR-0005)

Ostatni krok budowy:

```
C:\Windows\System32\Sysprep\sysprep.exe /generalize /oobe /shutdown /mode:vm /unattend:<clone-unattend.xml>
```

- `/mode:vm` — 18 s; klon pomija wykrywanie sprzętu.
- Uruchamiać synchronicznie (patrz wyżej).
- Gotowość klona: `IMAGE_STATE_COMPLETE` w `C:\Windows\Setup\State\State.ini` (sshd wstaje kilka sekund wcześniej). 53–61 s od startu.
- Spłaszczenie dysku po sysprepie (`qemu-img convert`) — 14 s.

Plik odpowiedzi klona: `ComputerName` = `*` w spike'u (docelowo nazwa maszyny z `.afk.toml`), ukryte OOBE, hasło Administratora, autologon `afk` (dla sterowania ekranem). Zawiera hasło jawnie — usuwać po starcie (`C:\Windows\Panther\unattend.xml` i kopia w `Sysprep`).

## Dlaczego sysprep: SID

Test na Server 2025 26100.7462: dwa klony bez sysprepu nie łączą się po SMB/NTLM (błąd 86, zdarzenie LSASRV 6167) — konto lokalne, workgroup, po IP, także po zmianie nazwy. Klon z sysprepem ↔ klon z sysprepem i klon ↔ obraz bez wspólnego pochodzenia działają. Zgodne z KB5070568 (Win11 24H2/25H2, Server 2025 po KB5064081/KB5065426). „The Machine SID Duplication Myth” (Russinovich, 2009) przestało obowiązywać dla tych wersji.

## OpenSSH na Win11

FoD `OpenSSH.Server` ma stan `NotPresent`. Próby: z sesji ssh — odmowa dostępu (WUA); jako SYSTEM (zadanie harmonogramu) — `0x8024402c`. Stąd MSI (ADR-0007). Starszy MSI v9.5.0.0p1-Beta też istnieje, wybrany 10.4.0.0p1-Preview.

## Czasy

| | |
|---|---|
| Budowa Server 2025 | 15,5 min |
| Budowa Win11 Pro | 14,2 min |
| Sysprep `/mode:vm` | 18 s |
| Spłaszczenie dysku | 14 s |
| Obraz | ok. 13 GB (bez kompaktowania) |

## Załącznik: `Autounattend.xml` (szablon `text/template`)

```xml
<?xml version="1.0" encoding="utf-8"?>
<unattend xmlns="urn:schemas-microsoft-com:unattend" xmlns:wcm="http://schemas.microsoft.com/WMIConfig/2002/State">
  <settings pass="windowsPE">
    <component name="Microsoft-Windows-International-Core-WinPE" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <SetupUILanguage><UILanguage>{{.Locale}}</UILanguage></SetupUILanguage>
      <InputLocale>{{.InputLocale}}</InputLocale>
      <SystemLocale>{{.Locale}}</SystemLocale>
      <UILanguage>{{.Locale}}</UILanguage>
      <UserLocale>{{.Locale}}</UserLocale>
    </component>
    <component name="Microsoft-Windows-PnpCustomizationsWinPE" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <DriverPaths>
{{- range $i, $p := .DriverPaths}}
        <PathAndCredentials wcm:action="add" wcm:keyValue="{{inc $i}}"><Path>{{$p}}</Path></PathAndCredentials>
{{- end}}
      </DriverPaths>
    </component>
    <component name="Microsoft-Windows-Setup" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
{{- if .LabConfig}}
      <RunSynchronous>
        <RunSynchronousCommand wcm:action="add"><Order>1</Order><Path>reg add HKLM\SYSTEM\Setup\LabConfig /v BypassSecureBootCheck /t REG_DWORD /d 1 /f</Path></RunSynchronousCommand>
      </RunSynchronous>
{{- end}}
      <DiskConfiguration>
        <Disk wcm:action="add">
          <DiskID>0</DiskID>
          <WillWipeDisk>true</WillWipeDisk>
          <CreatePartitions>
            <CreatePartition wcm:action="add"><Order>1</Order><Type>EFI</Type><Size>260</Size></CreatePartition>
            <CreatePartition wcm:action="add"><Order>2</Order><Type>MSR</Type><Size>16</Size></CreatePartition>
            <CreatePartition wcm:action="add"><Order>3</Order><Type>Primary</Type><Extend>true</Extend></CreatePartition>
          </CreatePartitions>
          <ModifyPartitions>
            <ModifyPartition wcm:action="add"><Order>1</Order><PartitionID>1</PartitionID><Format>FAT32</Format><Label>System</Label></ModifyPartition>
            <ModifyPartition wcm:action="add"><Order>2</Order><PartitionID>3</PartitionID><Format>NTFS</Format><Label>Windows</Label><Letter>C</Letter></ModifyPartition>
          </ModifyPartitions>
        </Disk>
      </DiskConfiguration>
      <ImageInstall>
        <OSImage>
          <InstallFrom><MetaData wcm:action="add"><Key>/IMAGE/INDEX</Key><Value>{{.ImageIndex}}</Value></MetaData></InstallFrom>
          <InstallTo><DiskID>0</DiskID><PartitionID>3</PartitionID></InstallTo>
        </OSImage>
      </ImageInstall>
      <UserData>
        <AcceptEula>true</AcceptEula>
{{- if .ProductKey}}
        <ProductKey><Key>{{.ProductKey}}</Key><WillShowUI>OnError</WillShowUI></ProductKey>
{{- end}}
      </UserData>
    </component>
  </settings>
  <settings pass="specialize">
    <component name="Microsoft-Windows-Shell-Setup" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <ComputerName>{{.ComputerName}}</ComputerName>
    </component>
    <component name="Microsoft-Windows-Deployment" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <RunSynchronous>
        <RunSynchronousCommand wcm:action="add">
          <Order>1</Order>
          <Path>cmd /c for %d in (D E F G H I J) do if exist %d:\afk-setup.ps1 powershell -NoProfile -ExecutionPolicy Bypass -File %d:\afk-setup.ps1 -Phase specialize -User {{.User}} -Password "{{.Password}}" -OsDir {{.OsDir}}</Path>
        </RunSynchronousCommand>
      </RunSynchronous>
    </component>
  </settings>
  <settings pass="oobeSystem">
    <component name="Microsoft-Windows-International-Core" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <InputLocale>{{.InputLocale}}</InputLocale>
      <SystemLocale>{{.Locale}}</SystemLocale>
      <UILanguage>{{.Locale}}</UILanguage>
      <UserLocale>{{.Locale}}</UserLocale>
    </component>
    <component name="Microsoft-Windows-Shell-Setup" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <OOBE>
        <HideEULAPage>true</HideEULAPage>
        <HideLocalAccountScreen>true</HideLocalAccountScreen>
        <HideOEMRegistrationScreen>true</HideOEMRegistrationScreen>
        <HideOnlineAccountScreens>true</HideOnlineAccountScreens>
        <HideWirelessSetupInOOBE>true</HideWirelessSetupInOOBE>
        <ProtectYourPC>3</ProtectYourPC>
      </OOBE>
      <UserAccounts>
        <AdministratorPassword><Value>{{.Password}}</Value><PlainText>true</PlainText></AdministratorPassword>
      </UserAccounts>
      <AutoLogon>
        <Enabled>true</Enabled>
        <Username>{{.User}}</Username>
        <Password><Value>{{.Password}}</Value><PlainText>true</PlainText></Password>
        <LogonCount>1</LogonCount>
      </AutoLogon>
      <FirstLogonCommands>
        <SynchronousCommand wcm:action="add">
          <Order>1</Order>
          <CommandLine>cmd /c for %d in (D E F G H I J) do if exist %d:\afk-setup.ps1 powershell -NoProfile -ExecutionPolicy Bypass -File %d:\afk-setup.ps1 -Phase firstlogon</CommandLine>
          <Description>afk enable sshd</Description>
        </SynchronousCommand>
      </FirstLogonCommands>
    </component>
  </settings>
</unattend>
```

`OsDir`: katalog w virtio-win (`2k25` dla Server 2025, `w11` dla Win11). `DriverPaths` w `windowsPE`: co najmniej `viostor` i `NetKVM` dla tego `OsDir`.

## Załącznik: `afk-setup.ps1`

```powershell
param([string]$Phase, [string]$User, [string]$Password, [string]$OsDir)
$ErrorActionPreference = 'Continue'
$cd = Split-Path -Parent $MyInvocation.MyCommand.Path
Start-Transcript -Path "C:\afk-setup-$Phase.log" -Append

if ($Phase -eq 'specialize') {
	# --- konto ---
	$secure = ConvertTo-SecureString $Password -AsPlainText -Force
	New-LocalUser -Name $User -Password $secure -PasswordNeverExpires -AccountNeverExpires | Out-Null
	Add-LocalGroupMember -SID 'S-1-5-32-544' -Member $User

	# --- sterowniki virtio ---
	foreach ($d in 'NetKVM', 'Balloon', 'vioserial', 'viorng', 'vioinput', 'viogpudo', 'pvpanic', 'qemupciserial') {
		$p = Join-Path $cd "$d\$OsDir\amd64"
		if (Test-Path $p) { pnputil /add-driver "$p\*.inf" /install | Select-Object -Last 1 | ForEach-Object { "driver ${d}: $_" } }
	}

	# --- klucz afk (sshd może jeszcze nie istnieć — plik i rejestr są niezależne od instalacji) ---
	New-Item -ItemType Directory -Force C:\ProgramData\ssh | Out-Null
	$keys = 'C:\ProgramData\ssh\administrators_authorized_keys'
	Copy-Item (Join-Path $cd 'authorized_keys') $keys -Force
	icacls $keys /inheritance:r /grant '*S-1-5-32-544:F' /grant '*S-1-5-18:F'
	New-Item -Force HKLM:\SOFTWARE\OpenSSH | Out-Null
	New-ItemProperty -Path HKLM:\SOFTWARE\OpenSSH -Name DefaultShell -Value "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe" -PropertyType String -Force | Out-Null

	# --- optymalizacje ---
	New-Item -Force 'HKLM:\SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate\AU' | Out-Null
	Set-ItemProperty 'HKLM:\SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate\AU' -Name NoAutoUpdate -Value 1 -Type DWord
	New-Item -Force 'HKLM:\SOFTWARE\Policies\Microsoft\Windows\DataCollection' | Out-Null
	Set-ItemProperty 'HKLM:\SOFTWARE\Policies\Microsoft\Windows\DataCollection' -Name AllowTelemetry -Value 0 -Type DWord
	foreach ($s in 'wuauserv', 'UsoSvc', 'WSearch', 'SysMain', 'DiagTrack') {
		Set-Service $s -StartupType Disabled -ErrorAction SilentlyContinue
	}
	powercfg /hibernate off
	Disable-ScheduledTask -TaskPath '\Microsoft\Windows\Defrag\' -TaskName ScheduledDefrag -ErrorAction SilentlyContinue | Out-Null
}

if ($Phase -eq 'firstlogon') {
	$p = Start-Process -Wait -PassThru -FilePath (Join-Path $cd 'virtio-win-guest-tools.exe') -ArgumentList '/install', '/quiet', '/norestart'
	"virtio guest tools: exit $($p.ExitCode)"

	$cap = Get-WindowsCapability -Online -Name 'OpenSSH.Server*' -ErrorAction SilentlyContinue
	if ($cap -and $cap.State -eq 'Installed') {
		'OpenSSH: built-in capability'
	} else {
		$p = Start-Process -Wait -PassThru msiexec.exe -ArgumentList '/i', (Join-Path $cd 'OpenSSH-Win64.msi'), '/qn', 'ADDLOCAL=Server'
		"OpenSSH: MSI exit $($p.ExitCode)"
	}
	New-NetFirewallRule -Name afk-sshd -DisplayName 'afk sshd' -Direction Inbound -Protocol TCP -LocalPort 22 -Action Allow -Profile Any -ErrorAction SilentlyContinue | Out-Null
	powercfg /setactive 8c5e7fda-e8bf-4a96-9a85-a6e23a8c635c
	Set-Service sshd -StartupType Automatic
	Start-Service sshd
}
Stop-Transcript
```
