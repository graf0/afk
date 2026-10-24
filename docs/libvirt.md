# libvirt

Połączenie `qemu:///system` przez `digitalocean/go-libvirt` (przypięty commit). Użytkownik `afk` musi mieć dostęp do gniazda libvirt i być w grupie `kvm`.

## Sandbox VM (pakiet `machine`)

Diagnostyka: `afk vm up` (start + ssh, wypisuje nazwę i czas), `afk vm shell <vm>` (shell z tunelem Egress proxy), `afk vm kill <vm>`.

- Nazwa `afk-<run|chat>-<id>` — wspólna dla domeny, sieci i nwfiltra; katalog `/var/lib/afk/{runs,chats}/<id>/`.
- `<metadata>` domeny (`xmlns:afk='https://github.com/graf0/afk/machine'`): Project, rodzaj, identyfikator, adres gościa, klucz hosta. To jedyny stan — `afk vm shell` czyta z niego, dokąd i z jakim kluczem się łączyć.
- Podsieć `10.99.<n>.0/24`, most `afkbr<n>`: najniższe `n` wolne wśród sieci libvirt (adres lub nazwa mostu). Most wyprowadzony z `n`, więc libvirt odrzuca drugą sieć na tym samym `n` już przy define („nazwa mostka … jest już używana”) → ponowna próba (max 5). Sprawdzone: 3 równoległe `vm up` dostały 0, 1, 2.
- `kill`: destroy + undefine domeny, sieci, nwfiltra (w tej kolejności — filtr w użyciu nie da się usunąć), `rm -rf` katalogu; brakujące części pomija. Nieudany lub anulowany `up` woła to samo (test: anulowanie w 0–2 s nie zostawia QEMU ani niczego w libvirt).
- Domena trwała (define + create), nie transient — po restarcie hosta zostaje wyłączona i `kill` ją sprząta.

## Klucz hosta per VM

Klony Base image mają wspólne klucze hosta sshd → TOFU nie wchodzi w grę. `afk` generuje ed25519 per VM i przypina go (`ssh.FixedHostKey`, `HostKeyAlgorithms` tylko ed25519; inny klucz → `ErrHostKey` bez ponawiania).

Dostarczenie: poświadczenie systemd przez SMBIOS (cloud-init w obrazie wyłączony):

```xml
<sysinfo type='smbios'><oemStrings>
  <entry>io.systemd.credential.binary:tmpfiles.extra=BASE64</entry>
</oemStrings></sysinfo>
<os>…<smbios mode='sysinfo'/></os>
```

`BASE64` to konfiguracja tmpfiles (`f+~` = utwórz lub nadpisz, argument w base64):

```
f+~ /etc/ssh/ssh_host_ed25519_key 0600 root root - <base64 PEM>
f+~ /etc/ssh/ssh_host_ed25519_key.pub 0644 root root - <base64 klucza publicznego>
```

`systemd-tmpfiles-setup.service` (przed `sysinit.target`) zapisuje klucz przed `ssh.service`. Sprawdzone w Debianie 13 (systemd 257): odcisk w gościu = odcisk z `<metadata>`. Klucz prywatny jest widoczny w XML domeny i w linii poleceń QEMU (`-smbios type=11`) — dla użytkowników hosta, nie dla innych VM.

## Dysk Run

```sh
qemu-img create -q -u -f qcow2 -F qcow2 -b /var/lib/afk/base/<name>/<img>.qcow2 <run>/disk.qcow2 64G
chmod g+rw <run>/disk.qcow2
```

- `-u`: nie otwieraj bazy (może być używana przez inne VM).
- Na każdym dysku `<seclabel model='dac' relabel='no'/>` w `<source>`. Bez tego libvirt robi chown bazy na `libvirt-qemu` 0640 i nie zawsze przywraca. Seclabel na całą domenę psuje swtpm (ADR-0001).

## Domena — referencja

Wspólne:

```xml
<domain type='kvm'>
  <memory unit='GiB'>2</memory><vcpu>2</vcpu>
  <os><type arch='x86_64' machine='q35'>hvm</type><boot dev='hd'/></os>
  <features><acpi/><apic/></features>
  <cpu mode='host-passthrough'/>
  <clock offset='utc'/>
  <devices>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2' discard='unmap'/>
      <source file='RUN/disk.qcow2'><seclabel model='dac' relabel='no'/></source>
      <target dev='vda' bus='virtio'/>
    </disk>
    <interface type='network'>
      <source network='afk-run-ID'/><mac address='52:54:00:af:00:0a'/>
      <model type='virtio'/><filterref filter='afk-run-ID'/>
    </interface>
    <memballoon model='virtio'><stats period='2'/></memballoon>
    <rng model='virtio'><backend model='random'>/dev/urandom</backend></rng>
    <channel type='unix'><target type='virtio' name='org.qemu.guest_agent.0'/></channel>
    <input type='tablet' bus='usb'/><input type='keyboard' bus='usb'/>
    <graphics type='vnc' port='-1' listen='127.0.0.1'/>
    <video><model type='virtio'/></video>
  </devices>
</domain>
```

Windows (UEFI) dodatkowo — 4 GiB RAM:

```xml
<os>
  <type arch='x86_64' machine='q35'>hvm</type>
  <loader readonly='yes' type='pflash'>/usr/share/OVMF/OVMF_CODE_4M.fd</loader>
  <nvram template='/var/lib/afk/base/NAME/efivars.fd'>RUN/VARS.fd</nvram>
</os>
<features><acpi/><apic/><hyperv mode='custom'>
  <relaxed state='on'/><vapic state='on'/><spinlocks state='on' retries='8191'/><vpindex state='on'/>
  <runtime state='on'/><synic state='on'/><stimer state='on'/><reset state='on'/>
  <frequencies state='on'/><tlbflush state='on'/><ipi state='on'/>
</hyperv></features>
<clock offset='localtime'><timer name='hypervclock' present='yes'/><timer name='hpet' present='no'/></clock>
```

Win11: `<tpm model='tpm-crb'><backend type='emulator' version='2.0'/></tpm>`. Sprzątanie: `undefine --nvram --tpm`.

Sprzęt musi odpowiadać buildowi Packera (q35, virtio, OVMF 4M, `cpu_model=host`).

## go-libvirt — wyniki

- Define/start/destroy/undefine, sieci, nwfilter, balloon, zdarzenia — działa.
- `DomainScreenshot` zwraca błąd XDR, ale pełny PNG jest w buforze strumienia → obejście: ignorować ten błąd, gdy bufor jest poprawnym PNG. Bug do zgłoszenia upstream.
- QMP `screendump` zwraca sukces, ale pliku nie ma — AppArmor blokuje zapis. Zasada: sukces QMP ≠ efekt.
- Balloon 4G → 2G na żywo działa (`DomainSetMemory`).
- Wejście: QMP `input-send-event` przez libvirt (absolutne współrzędne tabletu USB, przyciski) i `send-key` — klik w Start i wpisanie tekstu w Notatniku działa.
- Anulowanie kontekstu jest obowiązkowe — inaczej zostają osierocone procesy QEMU.

## Czasy (start → ssh)

| Obraz | Czas |
|---|---|
| Debian 13 | 9 s (spike); `afk vm up`: 6–8 s |
| Server 2025, klon bez sysprepu | 19–23 s |
| Win11 z vTPM, klon bez sysprepu | 19 s |
| Windows po sysprepie, do `IMAGE_STATE_COMPLETE` | 53–61 s |
