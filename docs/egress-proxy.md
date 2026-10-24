# Egress proxy

Jedyna droga ruchu wychodzącego z VM (ADR-0001). `afk` otwiera w sesji ssh odwrotny tunel (`client.Listen("tcp", "127.0.0.1:3128")` po stronie gościa) i obsługuje połączenia w procesie. W Run po stronie hosta nie ma żadnego portu. Przy budowie obrazu proxy słucha na losowym porcie loopbacku, a Packer tuneluje go `ssh_remote_tunnels` (ADR-0006).

## Protokół

HTTP proxy: `CONNECT` (HTTPS) i zwykłe HTTP. Gość: `http_proxy`/`https_proxy=http://127.0.0.1:3128`, apt przez `Acquire::http::Proxy`.

## Polityka

- Run: tylko domeny z `[network].allow` (domyślnie pusta) + domeny Agenta.
- Chat i budowa: wszystkie domeny.
- Porty: w trybie allowlisty (Run) dozwolone tylko 80 i 443 (`CONNECT` i zwykłe HTTP), inny port → 403, sprawdzane przed DNS. W trybie „wszystkie domeny” bez ograniczenia portów.
- Wpis allowlisty: nazwa, `*.domena` (podsubdomeny dowolnej głębokości, bez samej domeny) albo literał IP (dopasowanie dokładne, bez wildcardu). Wielkość liter ignorowana, końcowa kropka ucinana.
- Kolejność: port → nazwa na allowliście → DNS → kontrola adresów. Rozwiązanie nazwy spoza allowlisty to wyciek w zapytaniu DNS (`<sekret>.attacker.com`), nawet gdy połączenie zostanie odrzucone. Przy „wszystkich domenach” ten kanał jest i tak otwarty.
- Zawsze: cel rozwiązany na adres loopback/prywatny/link-local/unspecified/multicast albo adres interfejsu hosta → odmowa (403, także dla zwykłego HTTP). Sprawdzamy **wszystkie** adresy z DNS.
- Dodatkowe zakresy odmowy: CGNAT `100.64.0.0/10`, `0.0.0.0/8`, `240.0.0.0/4`, `192.0.0.0/24`, `198.18.0.0/15`, NAT64 `64:ff9b::/96` i `64:ff9b:1::/48`, 6to4 `2002::/16`, Teredo `2001::/32`, `::/96`, `fec0::/10`; IPv4-mapped (`::ffff:a.b.c.d`) jest odmapowywany i sprawdzany jako IPv4. NAT64 zostaje na liście, bo host ma IPv4; na hoście tylko z IPv6 i DNS64 proxy odrzuci wszystko.
- Odmowa też, gdy nie da się wylistować adresów hosta (fail closed) albo adres ma zone (`fe80::1%eth0`) → 403.
- Kody: 403 odmowa, 400 zły request (nie-proxy, `https://` w absolute-form, zły host:port), 502 błąd upstreamu (DNS, dial, `RoundTrip`).
- Zwykłe HTTP: usuwane `Proxy-Connection`, `Proxy-Authorization` i userinfo z URL; celem jest wyłącznie host z URL (nagłówek `Host` ignorowany).
- Łączymy się z rozwiązanym adresem, nie z nazwą (bez drugiego rozwiązania → bez DNS rebinding). IPv4 najpierw (IPv6 na hoście bywa bez trasy).

## Pułapka: bez `http.Server`

`http.Server` + `Hijack` na listenerze z kanału ssh się zakleszcza: Hijack wywołuje `SetReadDeadline`, którego `ssh.Channel` nie wspiera. Zamiast tego własna pętla — jedno żądanie na połączenie:

```go
br := bufio.NewReader(conn)
req, err := http.ReadRequest(br)
if req.Method == http.MethodConnect {
	upstream, derr := publicDial(ctx, "tcp", req.Host)
	// 403 przy derr
	fmt.Fprint(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
	go func() { io.Copy(upstream, br); upstream.Close() }() // br: najpierw to, co już zbuforowane
	io.Copy(conn, upstream)
	return
}
req.RequestURI = ""
req.Close = true
resp, err := transport.RoundTrip(req) // transport.DialContext = publicDial
resp.Close = true
resp.Write(conn)
```

Sprawdzone: HTTPS 200 do `deb.debian.org`, `apt install` przez proxy, odmowa dla loopbacku i LAN hosta przez proxy.
