# Egress proxy

Jedyna droga ruchu wychodzącego z VM (ADR-0001). `afk` otwiera w sesji ssh odwrotny tunel (`client.Listen("tcp", "127.0.0.1:3128")` po stronie gościa) i obsługuje połączenia w procesie. W Run po stronie hosta nie ma żadnego portu. Przy budowie obrazu proxy słucha na losowym porcie loopbacku, a Packer tuneluje go `ssh_remote_tunnels` (ADR-0006).

## Protokół

HTTP proxy: `CONNECT` (HTTPS) i zwykłe HTTP. Gość: `http_proxy`/`https_proxy=http://127.0.0.1:3128`, apt przez `Acquire::http::Proxy`.

## Polityka

- Run: tylko domeny z `[network].allow` (domyślnie pusta) + domeny Agenta.
- Chat i budowa: wszystkie domeny.
- Kolejność: nazwa na allowliście → DNS → kontrola adresów. Rozwiązanie nazwy spoza allowlisty to wyciek w zapytaniu DNS (`<sekret>.attacker.com`), nawet gdy połączenie zostanie odrzucone. Przy „wszystkich domenach” ten kanał jest i tak otwarty.
- Zawsze: cel rozwiązany na adres loopback/prywatny/link-local/unspecified/multicast albo adres interfejsu hosta → odmowa (403 dla `CONNECT`, w spike'u 502 dla zwykłego HTTP — błąd z `RoundTrip`; docelowo też 403). Sprawdzamy **wszystkie** adresy z DNS.
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

Sprawdzone: HTTPS 200 do `deb.debian.org`, `apt install` przez proxy, odmowa (502) dla loopbacku i LAN hosta przez proxy.
