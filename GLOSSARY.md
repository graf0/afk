# afk

Fabryka AI: bierze tickety z trackera i bez nadzoru implementuje je w kodzie, każdy w osobnej, odizolowanej maszynie wirtualnej. Część interaktywną (planowanie, eksperymenty) człowiek robi z agentem w czatach, też odizolowanych; wszystko, czego agent fabryki potrzebuje, jest w ticketach.

## Language

**Project**:
Repozytorium z Project config (bez niego `afk` nie pracuje); wyznacza Forge, Base image i allowlistę dla swoich Chatów i Factory.
_Avoid_: repo (gdy chodzi o całość z konfiguracją)

**Chat**:
Interaktywna rozmowa człowieka z Agentem we własnej, długo żyjącej Sandbox VM, do planowania i eksperymentów, z dostępem do całego internetu. Tickety tworzy przez `afk`, ale nigdy nie czyni ich gotowymi dla Factory — to robi człowiek.
_Avoid_: sesja, interactive session

**Export**:
Przeniesienie commitów Chat na branch `chat-<nazwa>` w Forge na żądanie człowieka, przez Quarantine repo i tę samą walidację co wynik Run; można je powtarzać, bez PR.
_Avoid_: sync, publish

**Factory**:
Nienadzorowana pętla, która bierze Tickety pasujące do selektora (`*` — kolejne gotowe, albo lista numerów) i każdy realizuje jako osobny Run.
_Avoid_: sesja, worker, ralph

**Ticket**:
Zadanie w trackerze z oznaczeniem `ready-for-agent`, zawierające wszystko, czego agent potrzebuje do pracy. Jedyne źródło wiedzy agenta o zadaniu.
_Avoid_: issue, task, zadanie

**Spec**:
Ticket oznaczony labelem Spec, z podrzędnymi ticketami (dziećmi); realizowany jako ciąg Runów na jednym branchu, jeden na dziecko, zakończony review. O tym, że ticket jest Spec, decyduje label, nie obecność dzieci.
_Avoid_: epic, parent

**Forge**:
System hostujący repozytorium, tickety i pull requesty (GitHub, Gitea). Tylko `afk` na hoście z nim rozmawia.
_Avoid_: tracker (gdy chodzi także o repo i PR), remote

**Run**:
Jedno podejście Factory do jednego ticketu (lub speca): od świeżej Sandbox VM do wyniku lub porażki.
_Avoid_: job, iteration

**Agent**:
Program AI kodujący (na razie Claude Code) uruchamiany w Sandbox VM. Nie ma dostępu do Forge.
_Avoid_: bot, worker

**Sandbox VM**:
Odizolowana maszyna wirtualna jednego Run albo jednego Chat, w której pracuje Agent. Nie ma drogi do świata poza kanałem kontrolowanym przez `afk`.
_Avoid_: agent VM, kontener

**Base image**:
Obraz dysku, z którego powstają maszyny; niezmienny, współdzielony, z narzędziami projektu.
_Avoid_: template (gdy chodzi o gotowy dysk), golden image

**Template**:
Wbudowany przepis, z którego `afk` buduje Base image (Debian, Windows).

**Tool**:
Nazwane narzędzie zadeklarowane w Project config, którego Agent może użyć (np. Postgres, Playwright, Windows do testów). Działa w Sandbox VM (domyślnie, od jej startu) albo w Tool VM (podnoszonej na żądanie Agenta). Agent dowiaduje się o nim tak samo bez względu na to, gdzie działa.
_Avoid_: capability, plugin, dependency, package

**Plugin**:
Rozszerzenie Claude Code (skille), które Project deklaruje w Project config, a `afk` instaluje w Base image. Daje Agentowi instrukcje, nie możliwości w środowisku — to nie Tool.
_Avoid_: skill (gdy chodzi o cały pakiet)

**Tool VM**:
Osobna maszyna wirtualna w sieci Run, hostująca jeden lub więcej Tooli; podnoszona na żądanie Agenta, ginie razem z Run. Agent łączy się z nią standardowymi protokołami lub przez sterowanie ekranem.

**Expiry**:
Ważność Base image zdefiniowana w projekcie; po jej upływie obraz jest budowany od nowa (Debian ze świeżego cloud image, Windows z ISO), zanim powstanie kolejna Sandbox VM.
_Avoid_: lifetime, TTL

**Gate**:
Polecenie bramki projektu (build, vet, test), które musi przejść, zanim wynik zostanie przyjęty.

**Quarantine repo**:
Repozytorium hosta, do którego trafia wynik z Sandbox VM; nigdy nie jest miejscem uruchamiania czegokolwiek.

**Egress proxy**:
Jedyna droga ruchu wychodzącego z Sandbox VM; przepuszcza tylko domeny dozwolone w konfiguracji projektu.

**Needs-info**:
Stan ticketu, w którym Agent nie mógł kontynuować i zadał pytanie; pytanie zapisuje w tickecie `afk`, nie Agent.
_Avoid_: blocked

**Project config**:
Plik `.afk.toml` w repozytorium projektu, który opisuje wszystko, czego `afk` potrzebuje do pracy nad nim.
