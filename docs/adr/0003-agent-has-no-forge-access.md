# Agent nie ma dostępu do Forge; pytania idą przez ticket; token Claude'a w VM

Host pobiera Ticket (treść, komentarze, spec, blokery) i zapisuje go w VM jako plik, którego ścieżkę podaje prompt. Prompt mówi wprost, że tracker jest celowo niedostępny i że przy braku informacji agent kończy Run ze statusem `needs_info` i listą pytań zamiast zgadywać. `afk` zapisuje pytania jako komentarz, ustawia label `needs-info` i zdejmuje `ready-for-agent`. Część interaktywna dzieje się przed fabryką, więc nie ma czatu z agentem ani `ask_human`.

Poświadczenie Claude'a (`CLAUDE_CODE_OAUTH_TOKEN`, jak w ralph.sh) trafia do VM jako zmienna środowiskowa sesji ssh, nigdy na dysk obrazu. Wstrzykiwanie klucza w proxy (czystsze, bez sekretu w VM) odrzucono: dokumentacja Anthropic wspiera je tylko dla klucza API, a subskrypcja (OAuth) przez pośrednika to szara strefa ToS.

## Consequences

Token w VM jest osiągalny dla agenta; chroni go tylko allowlista Egress proxy, która i tak przepuszcza `api.anthropic.com`. To świadome ryzyko.
Chat ma cały internet (bez tego research nic nie daje), więc w Chat token Claude'a i kod Projectu uznajemy za możliwe do wyniesienia: prompt injection ze strony może kazać je wysłać dokądkolwiek, a Anthropic sam przyznaje, że obrona modelu nie jest pewna, i nie daje mechanizmu, który by to zatrzymał przy `--dangerously-skip-permissions` (reguły allow nie działają, sandbox Claude Code nie obejmuje WebFetch, agent z sudo obchodzi wszystko w VM). Rozważone i odrzucone: osobny Chat „research” bez kodu (niewygodne) oraz token poza VM przez gateway w proxy (dla subskrypcji nieudokumentowane, regulamin zabrania pośredniczenia w poświadczeniach claude.ai). Dlatego Chat tworzy tickety przez `afk`, ale nigdy z labelem `ready-for-agent` — inaczej zatruta strona miałaby prostą drogę do nienadzorowanej Factory.
Maszyn nie niszczymy po czasie (nie wiadomo, ile co trwa), więc wiszący Run przedłuża ekspozycję tokenu. TUI pokazuje wiek Run i czas od ostatniego zdarzenia, a `afk kill` niszczy VM ręcznie.
