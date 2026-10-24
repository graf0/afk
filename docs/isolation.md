# Izolacja sieci

Decyzja: ADR-0001. Tu: konfiguracja i co sprawdziliśmy.

## Run: sieć libvirt + nwfilter

Sieć per Run, bez NAT (brak `<forward>`), DNS wyłączony, adresy z rezerwacji DHCP po MAC:

```xml
<network>
  <name>afk-RUN</name>
  <bridge name='virbr-afkX' stp='off'/>
  <dns enable='no'/>
  <ip address='10.99.0.1' netmask='255.255.255.0'>
    <dhcp><range start='10.99.0.100' end='10.99.0.199'/>
      <host mac='52:54:00:af:00:0a' ip='10.99.0.10'/>
    </dhcp>
  </ip>
</network>
```

nwfilter — **biała lista** (kolejność po `priority`):

```xml
<filter name='afk-RUN' chain='root'>
  <rule action='accept' direction='out' priority='90'><udp srcportstart='68' dstportstart='67'/></rule>
  <rule action='drop'   direction='out' priority='100'><all dstipaddr='10.99.0.1' state='NEW'/></rule>
  <rule action='accept' direction='out' priority='200'><all dstipaddr='10.99.0.0' dstipmask='24' state='NEW'/></rule>
  <rule action='drop'   direction='out' priority='300'><all state='NEW'/></rule>
  <rule action='drop'   direction='out' priority='310'><all-ipv6 state='NEW'/></rule>
  <rule action='accept' direction='inout' priority='500'><all/></rule>
</filter>
```

Interfejs domeny: `<filterref filter='afk-RUN'/>`.

### Wyniki

| Próba gościa | Wynik |
|---|---|
| host przez adres bridge'a | zablokowane |
| host przez adres LAN po `sudo ip route add` przez bridge | zablokowane (biała lista) |
| internet | zablokowane |
| inna VM tego Run | dozwolone |
| ssh host → gość, tunel `ssh -R` | działa |

**Wariant odrzucony:** filtr „drop NEW do 10.99.0.1, reszta accept” — gość z sudo dodał trasę przez host i dotarł do 192.168.0.204 (LAN IP hosta). Dlatego biała lista.

Na hoście `nft`, `iptables`, `ebtables`, `dnsmasq` są w `/usr/sbin` (nie w `PATH` zwykłego użytkownika — stąd wcześniejsza fałszywa diagnoza „brak narzędzi”).

## Budowa obrazu: QEMU user-mode

Packer uruchamia QEMU bezpośrednio, bez libvirt. Sieć:

```
-netdev user,id=user.0,restrict=on,hostfwd=tcp:127.0.0.1:<ssh_port>-:22
```

- `restrict=on` jest obowiązkowe: SLIRP i passt **bez** niego wpuszczają gościa do usług hosta (przez `10.0.2.2` i adres LAN).
- Z `restrict=on`: gość nie dociera ani do hosta, ani do internetu; działa tylko hostfwd ssh.
- Internet: `ssh_remote_tunnels = ["3128:127.0.0.1:<proxy_port>"]` → Egress proxy na loopbacku hosta, polityka „wszystkie domeny publiczne, bez adresów prywatnych i hosta”.

## Test integracyjny

Obowiązkowy (ADR-0001). Skrypt-sonda w gościu, Linux:

```sh
p() { (timeout 4 bash -c "echo > /dev/tcp/$1/$2" 2>/dev/null && echo "direct $1:$2 REACHABLE") || echo "direct $1:$2 blocked"; }
p 10.0.2.2 8100; p 192.168.0.204 8101; p 1.1.1.1 443
curl -s -m 10 -x http://127.0.0.1:3128 -o /dev/null -w '%{http_code}' https://deb.debian.org/debian/
```

Windows:

```powershell
function P($h, $p) { $c = New-Object Net.Sockets.TcpClient; try { if ($c.ConnectAsync($h, $p).Wait(4000)) { "direct ${h}:$p REACHABLE" } else { "direct ${h}:$p blocked" } } catch { "direct ${h}:$p blocked" } finally { $c.Dispose() } }
```

Host wystawia na czas testu nasłuchy na loopbacku i adresie LAN; test musi pokazać `blocked` dla wszystkich bezpośrednich i 200 przez proxy.
