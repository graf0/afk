# afk

Fabryka AI: bierze tickety z trackera i bez nadzoru implementuje je w kodzie, każdy w osobnym, odizolowanym Sandboxie obok własnego Stacku. Część interaktywną (planowanie, eksperymenty) człowiek robi z agentem w czatach, też odizolowanych; wszystko, czego agent fabryki potrzebuje, jest w ticketach.

## Language

**Project**:
Repozytorium z Project config (bez niego `afk` nie pracuje); wyznacza Forge, Base image i Stack dla swoich Chatów i Factory.
_Avoid_: repo (gdy chodzi o całość z konfiguracją)

**Chat**:
Interaktywna rozmowa człowieka z Agentem we własnym, długo żyjącym Sandboxie i Stacku, do planowania i eksperymentów, z dostępem do całego internetu. Pracuje na branchu `chat-<nazwa>`. Tickety tworzy przez `afk`, ale nigdy nie czyni ich gotowymi dla Factory — to robi człowiek.
_Avoid_: sesja, interactive session

**Export**:
Przeniesienie commitów Chat na branch `chat-<nazwa>` w Forge, przez Quarantine repo i tę samą walidację co wynik Run; można je powtarzać. Zleca je Agent, gdy otwiera PR z Chat.
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
Jedno podejście Factory do jednego ticketu (lub speca): od świeżego Sandboxa do wyniku lub porażki.
_Avoid_: job, iteration

**Agent**:
Program AI kodujący (na razie Claude Code) uruchamiany w Sandboxie. Do Forge sięga tylko przez wąskie operacje udostępnione przez `afk`.
_Avoid_: bot, worker

**Sandbox**:
Odizolowany kontener jednego Run albo jednego Chat, w którym pracuje Agent. Nie widzi sekretów hosta; poświadczeń hosta używa tylko przez kanał `afk`.
_Avoid_: Sandbox VM, kontener, instancja

**Stack**:
Środowisko jednego Chat albo Run: Sandbox oraz usługi Projectu (baza, serwer aplikacji, frontend), z którymi Agent rozmawia. Opisany w Projekcie, uruchamiany przez `afk` z gałęzi bazowej.
_Avoid_: instancja, compose

**VM testowa**:
Istniejąca maszyna wirtualna hosta zadeklarowana w Project config, którą Agent w Chat steruje (ekran, klawiatura, mysz, snapshoty) przez `afk`; w danej chwili trzyma ją co najwyżej jeden Chat. `afk` jej nie tworzy.
_Avoid_: Tool VM, test VM

**Base image**:
Obraz Sandboxa Projectu, budowany przez `afk` z gałęzi bazowej, gdy go brak; zmiana jego definicji na gałęzi bazowej oznacza nowy obraz.
_Avoid_: template, golden image

**Plugin**:
Rozszerzenie Claude Code (skille), które Project deklaruje w Project config, a `afk` udostępnia Agentowi w Sandboxie. Daje Agentowi instrukcje, nie możliwości w środowisku.
_Avoid_: skill (gdy chodzi o cały pakiet)

**Gate**:
Polecenie bramki projektu (build, vet, test), które musi przejść, zanim wynik zostanie przyjęty.

**Quarantine repo**:
Repozytorium hosta, do którego trafia wynik z Sandboxa; nigdy nie jest miejscem uruchamiania czegokolwiek.

**Needs-info**:
Stan ticketu, w którym Agent nie mógł kontynuować i zadał pytanie; pytanie zapisuje w tickecie `afk`, nie Agent.
_Avoid_: blocked

**Project config**:
Plik `.afk.toml` w repozytorium projektu, który opisuje wszystko, czego `afk` potrzebuje do pracy nad nim.
