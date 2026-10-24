---
status: superseded by ADR-0006
---

# Base image budujemy builderem qemu z Packera użytym jako biblioteka

Obrazy (Debian z cloud image, Windows z ISO i `Autounattend.xml`, warstwa projektu) buduje `afk` w procesie przez `github.com/hashicorp/packer-plugin-qemu/builder/qemu` i `packer-plugin-sdk` (MPL-2.0), wołając `Builder.Prepare`/`Builder.Run`. Builder daje gotową obsługę kruchych etapów: wysyłanie klawiszy przez VNC przy starcie, vTPM, zmienne EFI, czekanie na komunikator, shutdown, konwersję dysku. Provisioning robi własny hook po ssh (ten sam kanał i Egress proxy co w Run), więc nie używamy provisionerów z rdzenia Packera. Odrzuciliśmy Packera jako osobne narzędzie (BUSL 1.1, HCL jako drugi język konfiguracji, nie da się go przypiąć przez `go tool`, bo wymaga `replace` dla `go-cty`, który w zależnościach jest ignorowany) oraz własny generator od zera (te same przypadki brzegowe trzeba by przejść samemu).

## Consequences

`go.mod` potrzebuje `replace github.com/zclconf/go-cty => github.com/nywilken/go-cty` (w głównym module działa) i rośnie o ok. 150 zależności.
SDK jest przeznaczone dla autorów pluginów, nie do osadzania: brak gwarancji stabilności API, więc wersje pluginu i SDK są przypięte, a podbicie wymaga przebudowy wszystkich Template.
Builder uruchamia QEMU bezpośrednio, nie przez libvirt: sprzęt przy budowie (`cpu_model=host`, q35, virtio, OVMF) musi odpowiadać XML-owi libvirt przy uruchomieniu. Obraz UEFI to dysk plus `efivars.fd`, który jest szablonem NVRAM w libvirt.
Sieć budowy to `user,restrict=on` z hostfwd tylko dla ssh; internet wyłącznie przez Egress proxy w tunelu `ssh -R`, z polityką „wszystkie domeny, bez adresów prywatnych i hosta”. Sieć user-mode bez `restrict` (SLIRP, passt) wpuszcza gościa do usług hosta przez jego adres LAN.
Przerwanie `afk` musi anulować kontekst buildera; zabicie procesu bez tego zostawia osierocone QEMU.
