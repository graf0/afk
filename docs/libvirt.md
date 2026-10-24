# libvirt

Połączenie `qemu:///system` przez `digitalocean/go-libvirt`. Użytkownik `afk` musi mieć dostęp do gniazda libvirt i być w grupie `kvm`.

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
      <source network='afk-RUN'/><mac address='52:54:00:af:00:0a'/>
      <model type='virtio'/><filterref filter='afk-RUN'/>
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
| Debian 13 | 9 s |
| Server 2025, klon bez sysprepu | 19–23 s |
| Win11 z vTPM, klon bez sysprepu | 19 s |
| Windows po sysprepie, do `IMAGE_STATE_COMPLETE` | 53–61 s |
